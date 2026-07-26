package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sinacloud/vl"
	"sync"
	"time"

	"github.com/bdragon300/tusgo"
	warc "github.com/saveweb/gowarc"
	"github.com/saveweb/hq/pkg/protocol"
	"github.com/saveweb/hq/sdk/worker"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var flagDebufOutput string

func init() {
	flag.StringVar(&flagDebufOutput, "o", "", "debug output directory (for dev only)")
}

var client *warc.CustomHTTPClient
var logger *zap.Logger

const HQProject = "sinavideo"
const HQClientVersion = "sinavideo/2.4.0"

func main() {
	flag.Parse()

	HOSTNAME, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}

	USER_AGENT := "Mozilla/5.0 (compatible; saveweb) sinavideo_archive/020260620"
	WARCFilenameFeedbackChan := make(chan string, 5) // max 5 .warc + 1 .open + 1 uploading
	rotatorSettings := &warc.RotatorSettings{
		WarcinfoContent: warc.Header{
			"software":               []string{"saveweb_sinavideo_archive/020260620", "saveweb_gowarc/020260620"},
			"operator":               []string{"saveweb saveweb@saveweb.org"},
			"hostname":               []string{HOSTNAME},
			"http-header-user-agent": []string{USER_AGENT},
		},
		Prefix:                   "SINA_VIDEO",
		Compression:              warc.CompressionZstd,
		WARCWriterPoolSize:       1,
		OutputDirectory:          path.Join("./", "warcs"),
		WARCFilenameFeedbackChan: WARCFilenameFeedbackChan,
	}

	clientSettings := warc.HTTPClientSettings{
		RotatorSettings: rotatorSettings,
		TempDir:         path.Join("./", "temp"),
		DNSServers:      []string{"223.5.5.5", "1.1.1.1"},
		DedupeOptions: warc.DedupeOptions{
			LocalDedupe:   true,
			CDXDedupe:     false,
			SizeThreshold: 1024,
		},
		DialTimeout:             10 * time.Second,
		ResponseHeaderTimeout:   30 * time.Second,
		DNSResolutionTimeout:    5 * time.Second,
		DNSRecordsTTL:           30 * time.Minute,
		DNSCacheSize:            10000,
		MaxReadBeforeTruncate:   1000000000,
		DecompressBody:          true,
		FollowRedirects:         true,
		InsecureSkipVerifyCerts: false,
		RandomLocalIP:           true,

		EnableHTTP2:     false,
		EnableHTTP3:     false,
		EnableKeepAlive: true,

		DigestAlgorithm:  warc.BLAKE3,
		DefaultUserAgent: USER_AGENT,
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

	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject), zap.String("hostname", HOSTNAME)))

	hqURL := os.Getenv("HQ_TRACKER_URL")
	hqMachineToken := os.Getenv("HQ_MACHINE_TOKEN")
	if hqURL == "" || hqMachineToken == "" {
		logger.Fatal("HQ_TRACKER_URL and HQ_MACHINE_TOKEN must be set")
	}
	hqConfig := worker.Config{
		TrackerURL: hqURL, MachineToken: hqMachineToken,
		ClientVersion: HQClientVersion,
	}
	ctx := context.Background()
	userID, err := worker.WhoAmI(ctx, hqConfig)
	if err != nil {
		logger.Fatal("failed to resolve HQ user", zap.Error(err))
	}
	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", HQProject), zap.String("user_id", userID), zap.String("hostname", HOSTNAME)))

	tracker, err := worker.OpenProjectQueue(ctx, hqConfig, HQProject)
	if err != nil {
		logger.Fatal("failed to create tracker", zap.Error(err))
	}
	defer tracker.Close()
	logger.Info("opened HQ project queue", zap.String("worker_id", tracker.WorkerID()), zap.String("project", HQProject))

	pendingUploads := NewPendingUploads()

	var wg sync.WaitGroup

	wg.Go(func() {
		logger.Info("started warc uploader")
		for filename := range WARCFilenameFeedbackChan {
			logger.Info("uploading warc", zap.String("filename", filename))
			for {
				err := UploadWARC(filepath.Join("./warcs", filename), userID)
				if err != nil {
					logger.Error("failed to upload warc, wait 15s and retry", zap.Error(err))
					time.Sleep(15 * time.Second)
					continue
				}
				break
			}
			logger.Info("uploaded warc", zap.String("filename", filename))
			pendingUploads.OnWARCUploaded(filename)
		}
		logger.Info("warc uploader closed")
	})

	for {
		batch, err := tracker.Claim(ctx, worker.ClaimOptions{MaxJobs: 1, AcceptTypes: []string{protocol.JobTypeSeed}})
		if err != nil {
			logger.Fatal("failed to claim HQ job", zap.Error(err), zap.String("client_version", HQClientVersion))
		}
		if len(batch.Jobs) == 0 {
			break
		}
		job := batch.Jobs[0]
		vid := job.Spec.Value
		if !regexp.MustCompile(`^[0-9]+$`).MatchString(vid) {
			logger.Error("HQ job value is not a vid", zap.Int64("job", job.JobID), zap.String("value", vid))
			pendingUploads.AddJob(job, nil, protocol.Outcome{}, &worker.Failure{
				Retryable: false,
				Error:     protocol.ExecutionError{Code: "invalid_vid", Message: "job value must be a decimal vid", Details: protocol.Attrs{}},
			})
			continue
		}

		if client == nil {
			client, err = warc.NewWARCWritingHTTPClient(clientSettings)
			if err != nil {
				logger.Fatal("failed to create warc client", zap.Error(err))
			}
		}

		var outcome protocol.OutcomeKind
		records, err := archive(vid)
		var failure *worker.Failure
		if err != nil {
			logger.Error("failed to archive job", zap.Error(err))
			failure = &worker.Failure{Retryable: true, Error: protocol.ExecutionError{Code: "archive_failed", Message: err.Error(), Details: protocol.Attrs{}}}
		} else {
			logger.Info("archived job", zap.Int64("job", job.JobID), zap.String("vid", vid))
			outcome = protocol.OutcomeSuccess
		}
		pendingUploads.AddJob(job, records, protocol.Outcome{Kind: outcome, Meta: protocol.Attrs{"vid": vid}}, failure)

		metadataRecord := warc.NewRecord(client.TempDir)

		metadataRecord.Header.Set("WARC-Type", "metadata")
		metadataRecord.Header.Set("WARC-Target-URI", "urn:saveweb:"+HQProject+":"+vid)
		metadataRecord.Header.Set("Content-Type", "application/warc-fields")

		for _, record := range records {
			if record.RecordInfo.Header.Get("WARC-Type") == "response" {
				metadataRecord.Header.Add("WARC-Concurrent-To", record.RecordInfo.Header.Get("WARC-Record-ID"))
			}
		}

		metadataRecord.Content.Write([]byte("contributor: " + userID + "\n"))
		metadataRecord.Content.Write([]byte("SavewebJobOutcome: " + string(outcome) + "\n"))
		recordBatch := warc.NewRecordBatch(make(chan warc.FeedbackEvent, 1))
		recordBatch.Records = append(recordBatch.Records, metadataRecord)
		client.WARCWriter <- recordBatch
		// Wait for the metadata record to be written
		<-recordBatch.FeedbackChan

	}

	if client != nil {
		if err := client.Close(); err != nil {
			logger.Fatal("failed to close warc client", zap.Error(err))
		}
	}

	close(WARCFilenameFeedbackChan)

	wg.Wait()
}

type JobWithRecordInfoOutcome struct {
	job          *worker.Job
	RecordInfo   warc.RecordInfo
	WARCFilename string
}

type PendingUploads struct {
	RecordsByJob       map[int64][]*JobWithRecordInfoOutcome // job->[]unuploaded_records
	JobsByWARCFilename map[string][]int64                    // warcFilename->[]job
	CompletionByJob    map[int64]jobCompletion               // job::outcome
	mu                 sync.Mutex
}

type jobCompletion struct {
	job     *worker.Job
	outcome protocol.Outcome
	failure *worker.Failure
}

func NewPendingUploads() *PendingUploads {
	return &PendingUploads{
		RecordsByJob:       make(map[int64][]*JobWithRecordInfoOutcome),
		JobsByWARCFilename: make(map[string][]int64),
		CompletionByJob:    make(map[int64]jobCompletion),
	}
}

// Jobs without response records finish immediately. Others wait until every
// WARC containing their records has been uploaded.
func (pu *PendingUploads) AddJob(job *worker.Job, records []warc.RecordEvent, outcome protocol.Outcome, failure *worker.Failure) {
	pu.mu.Lock()
	defer pu.mu.Unlock()

	if len(records) == 0 {
		pu.finish(jobCompletion{job: job, outcome: outcome, failure: failure}, "fast path")
		return
	}

	for _, record := range records {
		pu.RecordsByJob[job.JobID] = append(pu.RecordsByJob[job.JobID], &JobWithRecordInfoOutcome{
			job:          job,
			RecordInfo:   record.RecordInfo,
			WARCFilename: record.WARCFilename,
		})
		pu.JobsByWARCFilename[record.WARCFilename] = append(pu.JobsByWARCFilename[record.WARCFilename], job.JobID)
	}
	pu.CompletionByJob[job.JobID] = jobCompletion{job: job, outcome: outcome, failure: failure}
}

func (pu *PendingUploads) OnWARCUploaded(warcFilename string) {
	pu.mu.Lock()
	defer pu.mu.Unlock()
	for _, job := range pu.JobsByWARCFilename[warcFilename] {
		completion := pu.CompletionByJob[job]
		jobRecordsTotal := len(pu.RecordsByJob[job]) // job 可能横跨多个 warcs 的 records 总数
		jobRecordsInsideWARC := 0                    // job 在此 warc 内的 records 总数
		jobRecordsOutsideWARC := 0                   // job 在此 warc 外的 records 总数

		jobRecordsOutside := []*JobWithRecordInfoOutcome{}
		for i, record := range pu.RecordsByJob[job] {
			if record.WARCFilename == warcFilename {
				jobRecordsInsideWARC++
			} else {
				jobRecordsOutsideWARC++
				jobRecordsOutside = append(jobRecordsOutside, pu.RecordsByJob[job][i])
			}
		}
		if jobRecordsOutsideWARC+jobRecordsInsideWARC != jobRecordsTotal {
			panic(fmt.Sprintf("job %d: jobRecordsOutsideWARC+jobRecordsInsideWARC != jobRecordsTotal (%d + %d != %d)", job, jobRecordsOutsideWARC, jobRecordsInsideWARC, jobRecordsTotal))
		}

		if jobRecordsTotal-jobRecordsInsideWARC == 0 {
			pu.finish(completion, "after WARC upload")
			delete(pu.RecordsByJob, job)
			delete(pu.CompletionByJob, job)
		} else {
			// job 还有未上传完成的 records，在其它 warcs 中
			// 移除已上传的 records，保留未上传的 records
			pu.RecordsByJob[job] = jobRecordsOutside
		}

	}
	delete(pu.JobsByWARCFilename, warcFilename)
}

func (pu *PendingUploads) Metrics() map[string]int {
	pu.mu.Lock()
	defer pu.mu.Unlock()
	metrics := make(map[string]int)
	for _, completion := range pu.CompletionByJob {
		metrics[string(completion.outcome.Kind)]++
	}
	return metrics
}

func (pu *PendingUploads) finish(completion jobCompletion, phase string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	if completion.failure != nil {
		err = completion.job.Fail(ctx, *completion.failure)
	} else {
		err = completion.job.Complete(ctx, completion.outcome)
	}
	if err != nil {
		logger.Error("failed to finalize HQ job", zap.Error(err), zap.Int64("job", completion.job.JobID), zap.String("phase", phase))
		return
	}
	logger.Info("HQ job finalized", zap.Int64("job", completion.job.JobID), zap.String("outcome", string(completion.outcome.Kind)), zap.String("phase", phase))
}

func UploadWARC(filepath, userID string) error {
	baseURL, _ := url.Parse("https://tus.saveweb.org/files")
	cl := tusgo.NewClient(http.DefaultClient, baseURL)

	f, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	metadata := make(map[string]string)
	metadata["filename"] = path.Base(f.Name())
	metadata["project"] = HQProject
	metadata["archivist"] = userID

	u := createUploadFromFile(f, cl, metadata)

	stream := tusgo.NewUploadStream(cl, u)
	if err := uploadWithRetry(stream, f); err != nil {
		return err
	}

	return nil
}

func uploadWithRetry(dst *tusgo.UploadStream, src *os.File) error {
	// Set stream and file pointer to be equal to the remote pointer
	// (if we resume the upload that was interrupted earlier)
	if _, err := dst.Sync(); err != nil {
		return err
	}
	if _, err := src.Seek(dst.Tell(), io.SeekStart); err != nil {
		return err
	}

	_, err := io.Copy(dst, src)
	attempts := 10
	for err != nil && attempts > 0 {
		if _, ok := err.(net.Error); !ok && !errors.Is(err, tusgo.ErrChecksumMismatch) {
			return err // Permanent error, no luck
		}
		time.Sleep(5 * time.Second)
		attempts--
		_, err = io.Copy(dst, src) // Try to resume the transfer again
	}
	if attempts == 0 {
		return errors.New("too many attempts to upload the data")
	}
	return nil
}

func createUploadFromFile(f *os.File, cl *tusgo.Client, metadata map[string]string) *tusgo.Upload {
	finfo, err := f.Stat()
	if err != nil {
		panic(err)
	}

	u := tusgo.Upload{}
	if _, err = cl.CreateUpload(&u, finfo.Size(), false, metadata); err != nil {
		panic(err)
	}
	return &u
}
