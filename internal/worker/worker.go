package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	cannerclient "github.com/saveweb/canner/client"
	"github.com/saveweb/hq/pkg/protocol"
	hqworker "github.com/saveweb/hq/sdk/worker"
	"go.uber.org/zap"

	sinaarchive "github.com/saveweb/sinavideo/internal/archive"
)

const (
	Project        = "sinavideo"
	clientVersion  = "sinavideo/1.4.3"
	finishTimeout  = 30 * time.Second
	uploadInterval = 30 * time.Second
)

var vidPattern = regexp.MustCompile(`^[0-9]+$`)

func Run(ctx context.Context, baseLogger *zap.Logger, maxJobs int) error {
	machineToken := os.Getenv("HQ_MACHINE_TOKEN")
	if machineToken == "" {
		return errors.New("HQ_MACHINE_TOKEN must be set")
	}
	canner, err := cannerclient.New(os.Getenv("CANNER_URL"))
	if err != nil {
		return fmt.Errorf("create canner client: %w", err)
	}

	config := hqworker.Config{MachineToken: machineToken, ClientVersion: clientVersion}
	userID, err := hqworker.WhoAmI(ctx, config)
	if err != nil {
		return fmt.Errorf("resolve HQ user: %w", err)
	}
	logger := baseLogger.With(zap.Dict("_stream", zap.String("project", Project), zap.String("gh", userID)))
	undoStdLog := zap.RedirectStdLog(logger)
	defer undoStdLog()

	queue, err := hqworker.OpenProjectQueue(ctx, config, Project)
	if err != nil {
		return fmt.Errorf("create tracker: %w", err)
	}
	defer queue.Close()
	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", Project), zap.String("gh", userID), zap.String("worker_id", queue.WorkerID())))
	undoStdLog = zap.RedirectStdLog(logger)
	defer undoStdLog()
	logger.Info("connected to HQ", zap.String("worker_id", queue.WorkerID()), zap.String("project", Project))

	return claimJobs(ctx, queue, canner, userID, maxJobs, logger)
}

func claimJobs(ctx context.Context, queue *hqworker.ProjectQueue, canner *cannerclient.Client, userID string, maxJobs int, logger *zap.Logger) error {
	claimedJobs := 0
	for {
		if ctx.Err() != nil {
			logger.Info("stopped before claiming another HQ job")
			return nil
		}
		if maxJobs > 0 && claimedJobs >= maxJobs {
			logger.Info("reached job limit", zap.Int("claimed_jobs", claimedJobs))
			return nil
		}
		batch, err := queue.Claim(ctx, hqworker.ClaimOptions{MaxJobs: 1, AcceptTypes: []string{protocol.JobTypeSeed}})
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("stopped while waiting for an HQ job")
				return nil
			}
			return fmt.Errorf("claim HQ job with %s: %w", clientVersion, err)
		}
		if len(batch.Jobs) == 0 {
			return nil
		}

		claimedJobs++
		if err := processJob(batch.Jobs[0], canner, userID, queue.WorkerID(), logger); err != nil {
			return err
		}
	}
}

func processJob(job *hqworker.Job, canner *cannerclient.Client, userID, workerID string, logger *zap.Logger) error {
	vid := job.Spec.Value
	if !vidPattern.MatchString(vid) {
		logger.Error("HQ job value is not a vid", zap.Int64("job", job.JobID), zap.String("value", vid))
		return finishJobFailure(job, hqworker.Failure{
			Retryable: false,
			Error:     protocol.ExecutionError{Code: "invalid_vid", Message: "job value must be a decimal vid", Details: protocol.Attrs{}},
		}, logger)
	}

	archiver := sinaarchive.New(logger, sinaarchive.DefaultOutputDirectory, sinaarchive.DefaultTempDirectory)
	warcPath, archiveErr, err := archiver.ArchiveJob(job.Context(), job.JobID, vid, userID, workerID)
	if err != nil {
		logger.Error("failed to create job WARC", zap.Error(err), zap.Int64("job", job.JobID), zap.String("vid", vid))
		return finishJobFailure(job, retryableFailure("warc_failed", err), logger)
	}
	if archiveErr != nil {
		logger.Error("failed to archive job", zap.Error(archiveErr), zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath))
		err := finishJobFailure(job, retryableFailure("archive_failed", archiveErr), logger)
		removeJobWARC(warcPath, job.JobID, logger)
		return err
	}

	warcSize, _ := fileSize(warcPath)
	logger.Info("uploading job WARC to canner", zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath), zap.Int64("size", warcSize))
	receipt, err := canner.UploadFileWithProgressToStdout(job.Context(), Project, warcPath, uploadInterval)
	if err != nil {
		logger.Error("failed to upload job WARC to canner", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath))
		return finishJobFailure(job, retryableFailure("artifact_upload_failed", err), logger)
	}
	removeJobWARC(warcPath, job.JobID, logger)

	outcome := protocol.Outcome{Kind: protocol.OutcomeSuccess, Meta: protocol.Attrs{"vid": vid}}
	if err := completeJob(job, outcome, artifactReceipt(receipt)); err != nil {
		if errors.Is(err, hqworker.ErrLeaseLost) {
			logger.Error("HQ job lease was lost before completion", zap.Error(err), zap.Int64("job", job.JobID), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
			return nil
		}
		return fmt.Errorf("complete HQ job %d with receipt %s: %w", job.JobID, receipt.ID, err)
	}
	logger.Info("completed HQ job", zap.Int64("job", job.JobID), zap.String("vid", vid), zap.String("warc", warcPath), zap.String("receipt", receipt.ID))
	return nil
}

func retryableFailure(code string, err error) hqworker.Failure {
	return hqworker.Failure{Retryable: true, Error: protocol.ExecutionError{Code: code, Message: err.Error(), Details: protocol.Attrs{}}}
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func removeJobWARC(path string, jobID int64, logger *zap.Logger) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Error("failed to remove job WARC", zap.Error(err), zap.Int64("job", jobID), zap.String("warc", path))
	}
}

func artifactReceipt(receipt cannerclient.Receipt) protocol.ArtifactReceipt {
	return protocol.ArtifactReceipt{
		ID: receipt.ID, Issuer: receipt.Issuer, ObjectID: receipt.ObjectID,
		Checksum: receipt.Checksum, SizeBytes: receipt.SizeBytes, AcceptedAt: receipt.AcceptedAt,
	}
}

func completeJob(job *hqworker.Job, outcome protocol.Outcome, receipt protocol.ArtifactReceipt) error {
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	return job.Complete(ctx, outcome, receipt)
}

func finishJobFailure(job *hqworker.Job, failure hqworker.Failure, logger *zap.Logger) error {
	if cause := context.Cause(job.Context()); cause != nil {
		logger.Error("cannot fail HQ job after losing its lease", zap.Error(cause), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	if err := job.Fail(ctx, failure); err != nil {
		if errors.Is(err, hqworker.ErrLeaseLost) {
			logger.Error("HQ job lease was lost before failure could be reported", zap.Error(err), zap.Int64("job", job.JobID), zap.String("code", failure.Error.Code))
			return nil
		}
		return fmt.Errorf("fail HQ job %d with code %s: %w", job.JobID, failure.Error.Code, err)
	}
	return nil
}
