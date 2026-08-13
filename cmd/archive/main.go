package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sinacloud/vl"
	"syscall"
	"time"

	cannerclient "github.com/saveweb/canner/client"
	warc "github.com/saveweb/gowarc"
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

var client *warc.CustomHTTPClient
var logger *zap.Logger

const (
	HQProject       = "sinavideo"
	HQClientVersion = "sinavideo/2.4.0"

	userAgent           = "Mozilla/5.0 (compatible; saveweb) sinavideo_archive/020260620"
	warcOutputDirectory = "warcs"
	warcTempDirectory   = "temp"
)

var vidPattern = regexp.MustCompile(`^[0-9]+$`)

func main() {
	flag.Parse()
	if flagMaxJobs < 0 {
		log.Fatal("max-jobs must not be negative")
	}

	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}

	vlWriter := vl.NewVLWriter(
		"https://victorialogs.saveweb.org/",
		"",
		10_000,
		500,
		2*time.Second,
	)
	defer vlWriter.Close()

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.MessageKey = "_msg"
	encoderConfig.TimeKey = "_time"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewTee(
		zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(os.Stdout), zap.InfoLevel),
		zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(vlWriter), zap.InfoLevel),
	)

	baseLogger := zap.New(core, zap.AddCaller())
	defer baseLogger.Sync()

	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject), zap.String("hostname", hostname)))
	shutdownCtx, stopShutdown := gracefulShutdownContext(logger)
	defer stopShutdown()

	hqMachineToken := os.Getenv("HQ_MACHINE_TOKEN")
	cannerURL := os.Getenv("CANNER_URL")
	if hqMachineToken == "" {
		logger.Fatal("HQ_MACHINE_TOKEN must be set")
	}

	canner, err := cannerclient.New(cannerURL)
	if err != nil {
		logger.Fatal("failed to create canner client", zap.Error(err))
	}

	hqConfig := worker.Config{
		MachineToken:  hqMachineToken,
		ClientVersion: HQClientVersion,
	}
	userID, err := worker.WhoAmI(shutdownCtx, hqConfig)
	if err != nil {
		logger.Fatal("failed to resolve HQ user", zap.Error(err))
	}
	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject), zap.String("user_id", userID), zap.String("hostname", hostname)))
	undoStdLog := zap.RedirectStdLog(logger)
	defer undoStdLog()

	tracker, err := worker.OpenProjectQueue(context.Background(), hqConfig, HQProject)
	if err != nil {
		logger.Fatal("failed to create tracker", zap.Error(err))
	}
	defer tracker.Close()
	logger.Info("opened HQ project queue", zap.String("worker_id", tracker.WorkerID()), zap.String("project", HQProject))

	claimedJobs := 0
	for {
		if shutdownCtx.Err() != nil {
			logger.Info("stopped before claiming another HQ job")
			return
		}
		if flagMaxJobs > 0 && claimedJobs >= flagMaxJobs {
			logger.Info("reached job limit", zap.Int("claimed_jobs", claimedJobs))
			return
		}
		batch, err := tracker.Claim(shutdownCtx, worker.ClaimOptions{MaxJobs: 1, AcceptTypes: []string{protocol.JobTypeSeed}})
		if err != nil {
			if shutdownCtx.Err() != nil {
				logger.Info("stopped while waiting for an HQ job")
				return
			}
			logger.Fatal("failed to claim HQ job", zap.Error(err), zap.String("client_version", HQClientVersion))
		}
		if len(batch.Jobs) == 0 {
			return
		}

		job := batch.Jobs[0]
		claimedJobs++
		vid := job.Spec.Value
		if !vidPattern.MatchString(vid) {
			logger.Error("HQ job value is not a vid", zap.Int64("job", job.JobID), zap.String("value", vid))
			finishJobFailure(job, worker.Failure{
				Retryable: false,
				Error:     protocol.ExecutionError{Code: "invalid_vid", Message: "job value must be a decimal vid", Details: protocol.Attrs{}},
			})
			continue
		}

		warcPath, archiveErr, err := archiveJob(job.Context(), job.JobID, vid, userID, hostname)
		if err != nil {
			logger.Error("failed to create job WARC", zap.Error(err), zap.Int64("job", job.JobID), zap.String("vid", vid))
			finishJobFailure(job, worker.Failure{
				Retryable: true,
				Error:     protocol.ExecutionError{Code: "warc_failed", Message: err.Error(), Details: protocol.Attrs{}},
			})
			continue
		}
		if archiveErr != nil {
			logger.Error("failed to archive job", zap.Error(archiveErr), zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath))
			finishJobFailure(job, worker.Failure{
				Retryable: true,
				Error:     protocol.ExecutionError{Code: "archive_failed", Message: archiveErr.Error(), Details: protocol.Attrs{}},
			})
			continue
		}

		logger.Info("uploading job WARC to canner", zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath))
		receipt, err := canner.UploadFile(job.Context(), HQProject, warcPath)
		if err != nil {
			logger.Error("failed to upload job WARC to canner", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath))
			finishJobFailure(job, worker.Failure{
				Retryable: true,
				Error:     protocol.ExecutionError{Code: "artifact_upload_failed", Message: err.Error(), Details: protocol.Attrs{}},
			})
			continue
		}
		if err := os.Remove(warcPath); err != nil {
			logger.Error("failed to remove uploaded job WARC", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath))
		}

		outcome := protocol.Outcome{Kind: protocol.OutcomeSuccess, Meta: protocol.Attrs{"vid": vid}}
		if err := completeJob(job, outcome, artifactReceipt(receipt)); err != nil {
			if errors.Is(err, worker.ErrLeaseLost) {
				logger.Error("HQ job lease was lost before completion", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
				continue
			}
			logger.Fatal("failed to complete HQ job", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
		}
		logger.Info("completed HQ job", zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
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
		logger.Info("shutdown requested; finishing the current HQ job before stopping", zap.String("force_exit", "press Ctrl-C again"))
	}()
	return ctx, func() {
		signal.Stop(shutdownSignals)
		cancel()
	}
}

func newWARCClientSettings(hostname string, jobID int64, outputDirectory, tempDirectory string) warc.HTTPClientSettings {
	rotatorSettings := warc.NewRotatorSettings(hostname)
	rotatorSettings.WarcinfoContent.Set("software", "saveweb_sinavideo_archive/020260620")
	rotatorSettings.WarcinfoContent.Add("software", "saveweb_gowarc/020260620")
	rotatorSettings.WarcinfoContent.Set("operator", "saveweb saveweb@saveweb.org")
	rotatorSettings.WarcinfoContent.Set("http-header-user-agent", userAgent)
	rotatorSettings.Prefix = fmt.Sprintf("SINA_VIDEO_%d", jobID)
	rotatorSettings.Compression = warc.CompressionZstd
	rotatorSettings.WARCSize = math.MaxFloat64
	rotatorSettings.WARCWriterPoolSize = 1
	rotatorSettings.OutputDirectory = outputDirectory

	return warc.HTTPClientSettings{
		RotatorSettings: rotatorSettings,
		TempDir:         tempDirectory,
		DNSServers:      []string{"223.5.5.5", "1.1.1.1"},
		DedupeOptions: warc.DedupeOptions{
			LocalDedupe:   true,
			CDXDedupe:     false,
			SizeThreshold: 1024,
		},
		DialTimeout:             10 * time.Second,
		ResponseHeaderTimeout:   30 * time.Second,
		ConnReadDeadline:        30 * time.Second,
		DNSResolutionTimeout:    5 * time.Second,
		DNSRecordsTTL:           30 * time.Minute,
		DNSCacheSize:            10000,
		DecompressBody:          true,
		FollowRedirects:         true,
		InsecureSkipVerifyCerts: false,
		RandomLocalIP:           true,
		EnableHTTP2:             false,
		EnableHTTP3:             false,
		DisableKeepAlives:       false,
		DigestAlgorithm:         warc.BLAKE3,
		DefaultUserAgent:        userAgent,
	}
}

// archiveJob owns the complete lifecycle of exactly one WARC writer. Disabling
// size rotation ensures no job can be split across multiple artifact files.
func archiveJob(ctx context.Context, jobID int64, vid, userID, hostname string) (warcPath string, archiveErr, err error) {
	return archiveJobTo(ctx, jobID, vid, userID, hostname, warcOutputDirectory, warcTempDirectory)
}

func archiveJobTo(ctx context.Context, jobID int64, vid, userID, hostname, outputDirectory, tempDirectory string) (warcPath string, archiveErr, err error) {
	settings := newWARCClientSettings(hostname, jobID, outputDirectory, tempDirectory)

	warcClient, err := warc.NewWARCWritingHTTPClient(settings)
	if err != nil {
		return "", nil, err
	}
	client = warcClient
	defer func() { client = nil }()

	records, archiveErr := archive(ctx, vid)
	outcome := string(protocol.OutcomeSuccess)
	if archiveErr != nil {
		outcome = "failed"
	}
	metadataErr := writeJobMetadata(warcClient, vid, userID, outcome, records)
	// Network work obeys ctx, but a canceled lease must not leave an open WARC.
	// Shutdown waits for already-owned capture work and finalizes the local file.
	finalizeResult, finalizeErr := warcClient.Shutdown(context.Background())
	if err := errors.Join(metadataErr, finalizeErr); err != nil {
		return "", archiveErr, err
	}

	filenames := finalizeResult.FinalizedFiles
	if len(filenames) != 1 {
		return "", archiveErr, fmt.Errorf("job %d produced %d WARC files, want exactly one", jobID, len(filenames))
	}
	return filepath.Join(outputDirectory, filenames[0]), archiveErr, nil
}

func writeJobMetadata(warcClient *warc.CustomHTTPClient, vid, userID, outcome string, records []warc.RecordEvent) error {
	metadataRecord := warc.NewRecord(warcClient.TempDir)
	metadataRecord.Header.Set("WARC-Type", "metadata")
	metadataRecord.Header.Set("WARC-Target-URI", "urn:saveweb:"+HQProject+":"+vid)
	metadataRecord.Header.Set("Content-Type", "application/warc-fields")

	for _, record := range records {
		if record.RecordInfo.Header.Get("WARC-Type") == "response" {
			metadataRecord.Header.Add("WARC-Concurrent-To", record.RecordInfo.Header.Get("WARC-Record-ID"))
		}
	}
	if _, err := metadataRecord.Content.Write([]byte("contributor: " + userID + "\n")); err != nil {
		return err
	}
	if _, err := metadataRecord.Content.Write([]byte("SavewebJobOutcome: " + outcome + "\n")); err != nil {
		return err
	}

	recordBatch := warc.NewRecordBatch(nil)
	recordBatch.Records = append(recordBatch.Records, metadataRecord)
	// Failure metadata is still useful when ctx was canceled, so finalization
	// owns this writer operation rather than the request lifecycle.
	result, err := warcClient.WriteBatch(context.Background(), recordBatch)
	if err != nil {
		return err
	}
	if len(result.Events) != 1 {
		return fmt.Errorf("metadata writer returned %d records, want one", len(result.Events))
	}
	return nil
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

func finishJobFailure(job *worker.Job, failure worker.Failure) {
	if cause := context.Cause(job.Context()); cause != nil {
		logger.Error("cannot fail HQ job after losing its lease", zap.Error(cause), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := job.Fail(ctx, failure); err != nil {
		if errors.Is(err, worker.ErrLeaseLost) {
			logger.Error("HQ job lease was lost before failure could be reported", zap.Error(err), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
			return
		}
		logger.Fatal("failed to fail HQ job", zap.Error(err), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
	}
}
