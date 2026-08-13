package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	sinaarchive "sinacloud/internal/archive"
	"sinacloud/vl"
	"syscall"
	"time"

	cannerclient "github.com/saveweb/canner/client"
	"github.com/saveweb/hq/pkg/protocol"
	"github.com/saveweb/hq/sdk/worker"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var flagDebufOutput string
var flagMaxJobs int

func init() {
	flag.StringVar(&flagDebufOutput, "o", "", "debug output directory (for dev only)")
	flag.IntVar(&flagMaxJobs, "max-jobs", 0, "maximum jobs to claim before stopping (0 means unlimited)")
}

var logger *zap.Logger

const (
	HQProject       = "sinavideo"
	HQClientVersion = "sinavideo/2.4.0"

	uploadLogInterval = 30 * time.Second
)

var vidPattern = regexp.MustCompile(`^[0-9]+$`)

func main() {
	flag.Parse()
	if flagMaxJobs < 0 {
		log.Fatal("max-jobs must not be negative")
	}
	baseLogger, closeLogger := newLogger()
	defer closeLogger()

	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject)))
	shutdownCtx, stopShutdown := gracefulShutdownContext(logger)
	defer stopShutdown()

	if err := runWorker(shutdownCtx, baseLogger); err != nil {
		logger.Fatal("archive worker stopped", zap.Error(err))
	}
}

func newLogger() (*zap.Logger, func()) {
	vlWriter := vl.NewVLWriter(
		"https://victorialogs.saveweb.org/",
		"",
		10_000,
		500,
		2*time.Second,
	)

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.MessageKey = "_msg"
	encoderConfig.TimeKey = "_time"
	encoderConfig.EncodeTime = utcISO8601TimeEncoder

	core := zapcore.NewTee(
		omitFields(
			zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(os.Stdout), zap.InfoLevel),
			"_stream",
		),
		zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(vlWriter), zap.InfoLevel),
	)

	baseLogger := zap.New(core, zap.AddCaller())
	return baseLogger, func() {
		_ = baseLogger.Sync()
		vlWriter.Close()
	}
}

func runWorker(ctx context.Context, baseLogger *zap.Logger) error {
	hqMachineToken := os.Getenv("HQ_MACHINE_TOKEN")
	cannerURL := os.Getenv("CANNER_URL")
	if hqMachineToken == "" {
		return errors.New("HQ_MACHINE_TOKEN must be set")
	}

	canner, err := cannerclient.New(cannerURL)
	if err != nil {
		return fmt.Errorf("create canner client: %w", err)
	}

	hqConfig := worker.Config{
		MachineToken:  hqMachineToken,
		ClientVersion: HQClientVersion,
	}
	userID, err := worker.WhoAmI(ctx, hqConfig)
	if err != nil {
		return fmt.Errorf("resolve HQ user: %w", err)
	}
	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject), zap.String("gh", userID)))
	undoStdLog := zap.RedirectStdLog(logger)
	defer undoStdLog()

	tracker, err := worker.OpenProjectQueue(ctx, hqConfig, HQProject)
	if err != nil {
		return fmt.Errorf("create tracker: %w", err)
	}
	defer tracker.Close()
	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject), zap.String("gh", userID), zap.String("worker_id", tracker.WorkerID())))
	undoStdLog = zap.RedirectStdLog(logger)
	defer undoStdLog()
	logger.Info("connected to HQ", zap.String("worker_id", tracker.WorkerID()), zap.String("project", HQProject))

	return claimJobs(ctx, tracker, canner, userID)
}

func claimJobs(ctx context.Context, tracker *worker.ProjectQueue, canner *cannerclient.Client, userID string) error {
	claimedJobs := 0
	for {
		if ctx.Err() != nil {
			logger.Info("stopped before claiming another HQ job")
			return nil
		}
		if flagMaxJobs > 0 && claimedJobs >= flagMaxJobs {
			logger.Info("reached job limit", zap.Int("claimed_jobs", claimedJobs))
			return nil
		}
		batch, err := tracker.Claim(ctx, worker.ClaimOptions{MaxJobs: 1, AcceptTypes: []string{protocol.JobTypeSeed}})
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("stopped while waiting for an HQ job")
				return nil
			}
			return fmt.Errorf("claim HQ job with %s: %w", HQClientVersion, err)
		}
		if len(batch.Jobs) == 0 {
			return nil
		}

		job := batch.Jobs[0]
		claimedJobs++
		if err := processJob(job, canner, userID, tracker.WorkerID()); err != nil {
			return err
		}
	}
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func processJob(job *worker.Job, canner *cannerclient.Client, userID, workerID string) error {
	vid := job.Spec.Value
	if !vidPattern.MatchString(vid) {
		logger.Error("HQ job value is not a vid", zap.Int64("job", job.JobID), zap.String("value", vid))
		return finishJobFailure(job, worker.Failure{
			Retryable: false,
			Error:     protocol.ExecutionError{Code: "invalid_vid", Message: "job value must be a decimal vid", Details: protocol.Attrs{}},
		})
	}

	archiver := sinaarchive.New(logger, sinaarchive.DefaultOutputDirectory, sinaarchive.DefaultTempDirectory)
	warcPath, archiveErr, err := archiver.ArchiveJob(job.Context(), job.JobID, vid, userID, workerID)
	if err != nil {
		logger.Error("failed to create job WARC", zap.Error(err), zap.Int64("job", job.JobID), zap.String("vid", vid))
		return finishJobFailure(job, worker.Failure{
			Retryable: true,
			Error:     protocol.ExecutionError{Code: "warc_failed", Message: err.Error(), Details: protocol.Attrs{}},
		})
	}

	if archiveErr != nil {
		logger.Error("failed to archive job", zap.Error(archiveErr), zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath))
		err := finishJobFailure(job, worker.Failure{
			Retryable: true,
			Error:     protocol.ExecutionError{Code: "archive_failed", Message: archiveErr.Error(), Details: protocol.Attrs{}},
		})
		removeJobWARC(warcPath, job.JobID)
		return err
	}

	warcSize, _ := fileSize(warcPath)

	logger.Info("uploading job WARC to canner", zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath), zap.Int64("size", warcSize))
	receipt, err := canner.UploadFileWithProgressToStdout(job.Context(), HQProject, warcPath, uploadLogInterval)
	if err != nil {
		logger.Error("failed to upload job WARC to canner", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath))
		return finishJobFailure(job, worker.Failure{
			Retryable: true,
			Error:     protocol.ExecutionError{Code: "artifact_upload_failed", Message: err.Error(), Details: protocol.Attrs{}},
		})
	}
	removeJobWARC(warcPath, job.JobID)

	outcome := protocol.Outcome{Kind: protocol.OutcomeSuccess, Meta: protocol.Attrs{"vid": vid}}
	if err := completeJob(job, outcome, artifactReceipt(receipt)); err != nil {
		if errors.Is(err, worker.ErrLeaseLost) {
			logger.Error("HQ job lease was lost before completion", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
			return nil
		}
		return fmt.Errorf("complete HQ job %d with receipt %s: %w", job.JobID, receipt.ID, err)
	}
	logger.Info("completed HQ job", zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
	return nil
}

func removeJobWARC(warcPath string, jobID int64) {
	if err := os.Remove(warcPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Error("failed to remove job WARC", zap.Error(err), zap.Int64("job", jobID), zap.String("warc", warcPath))
	}
}

func gracefulShutdownContext(logger *zap.Logger) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdownSignals
		signal.Stop(shutdownSignals)
		cancel()
		logger.Info("shutdown requested; canceling the current HQ job and stopping", zap.String("force_exit", "press Ctrl-C again"))
	}()
	return ctx, func() {
		signal.Stop(shutdownSignals)
		cancel()
	}
}

func artifactReceipt(receipt cannerclient.Receipt) protocol.ArtifactReceipt {
	return protocol.ArtifactReceipt{
		ID:         receipt.ID,
		Issuer:     receipt.Issuer,
		ObjectID:   receipt.ObjectID,
		Checksum:   receipt.Checksum,
		SizeBytes:  receipt.SizeBytes,
		AcceptedAt: receipt.AcceptedAt,
	}
}

func completeJob(job *worker.Job, outcome protocol.Outcome, receipt protocol.ArtifactReceipt) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return job.Complete(ctx, outcome, receipt)
}

func finishJobFailure(job *worker.Job, failure worker.Failure) error {
	if cause := context.Cause(job.Context()); cause != nil {
		logger.Error("cannot fail HQ job after losing its lease", zap.Error(cause), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := job.Fail(ctx, failure); err != nil {
		if errors.Is(err, worker.ErrLeaseLost) {
			logger.Error("HQ job lease was lost before failure could be reported", zap.Error(err), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
			return nil
		}
		return fmt.Errorf("fail HQ job %d with code %s: %w", job.JobID, failure.Error.Code, err)
	}
	return nil
}
