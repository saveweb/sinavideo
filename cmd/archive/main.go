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
	bitclient "github.com/saveweb/bit_client"
	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var flagDebufOutput string
var ARCHIVIST string

func init() {
	flag.StringVar(&flagDebufOutput, "o", "", "debug output directory (for dev only)")
	flag.StringVar(&ARCHIVIST, "", "archivist", "archivist name")
}

var client = &warc.CustomHTTPClient{}
var logger *zap.Logger

const PROJECT = "sinavideo_64"

func main() {
	flag.Parse()

	HOSTNAME, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}

	if ARCHIVIST == "" {
		ARCHIVIST = os.Getenv("ARCHIVIST")
		if ARCHIVIST == "" {
			log.Fatal("-archivist not set and ARCHIVIST environment variable not set")
		}
	}

	if len(ARCHIVIST) > 20 {
		log.Fatal("archivist must be 20 characters or less")
	}

	// ARCHIVIST only allows a-z0-9 and - and _
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(ARCHIVIST) {
		log.Fatal("-archivist must be alphanumeric and may contain - and _")
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

	logger = baseLogger.With(zap.Dict("_stream", zap.String("project", PROJECT), zap.String("archivist", ARCHIVIST), zap.String("hostname", HOSTNAME)))

	tracker, err := bitclient.NewClient(
		bitclient.WithBaseURL("https://bittracker.saveweb.org"),
		bitclient.WithProject(PROJECT),
		bitclient.WithUserAgent("sinavideo/1.0"),
	)
	if err != nil {
		logger.Fatal("failed to create tracker", zap.Error(err))
	}
	defer tracker.Close()

	ctx := context.Background()
	if err := tracker.Connect(ctx); err != nil { // fetches QoS, starts refresh loop
		logger.Fatal("failed to connect to tracker", zap.Error(err))
	}

	pendingUploads := &PendingUploads{}

	var wg sync.WaitGroup

	wg.Go(func() {
		logger.Info("started warc uploader")
		for filename := range WARCFilenameFeedbackChan {
			logger.Info("uploading warc", zap.String("filename", filename))
			for {
				err := UploadWARC(filepath.Join("./warcs", filename), ARCHIVIST)
				if err != nil {
					logger.Error("failed to upload warc, wait 15s and retry", zap.Error(err))
					time.Sleep(15 * time.Second)
					continue
				}
				break
			}
			logger.Info("uploaded warc", zap.String("filename", filename))
			pendingUploads.OnWARCUploaded(filename, tracker)
		}
		logger.Info("warc uploader closed")
	})

	for {
		job, err := tracker.Claim(ctx)
		if err != nil {
			if errors.Is(err, bitclient.ErrNoBitsToClaim) {
				break
			}
			logger.Error("failed to claim job", zap.Error(err))
			continue
		}

		if client == nil {
			client, err = warc.NewWARCWritingHTTPClient(clientSettings)
			if err != nil {
				logger.Fatal("failed to create warc client", zap.Error(err))
			}
		}

		var outcome string
		records, err := archive(fmt.Sprintf("%d", job))
		if err != nil {
			logger.Error("failed to archive job", zap.Error(err))
			outcome = bitclient.MapFail
		} else {
			logger.Info("archived job", zap.Uint64("job", job))
			outcome = bitclient.MapDone
		}
		pendingUploads.AddJob(job, records, bitclient.MapFail, tracker)

		metadataRecord := warc.NewRecord(client.TempDir)

		metadataRecord.Header.Set("WARC-Type", "metadata")
		metadataRecord.Header.Set("WARC-Target-URI", "urn:saveweb:"+PROJECT+":"+fmt.Sprintf("%d", job))
		metadataRecord.Header.Set("Content-Type", "application/warc-fields")

		for _, record := range records {
			if record.RecordInfo.Header.Get("WARC-Type") == "response" {
				metadataRecord.Header.Add("WARC-Concurrent-To", record.RecordInfo.Header.Get("WARC-Record-ID"))
			}
		}

		metadataRecord.Content.Write([]byte("contributor: " + ARCHIVIST + "\n"))
		metadataRecord.Content.Write([]byte("SavewebJobOutcome: " + outcome + "\n"))
		batch := warc.NewRecordBatch(make(chan warc.FeedbackEvent, 1))
		batch.Records = append(batch.Records, metadataRecord)
		client.WARCWriter <- batch
		// Wait for the metadata record to be written
		<-batch.FeedbackChan

	}

	if client != nil {
		if err := client.Close(); err != nil {
			logger.Fatal("failed to close warc client", zap.Error(err))
		}
	}

	wg.Wait()
}

type JobWithRecordInfoOutcome struct {
	uint64
	RecordInfo   warc.RecordInfo
	WARCFilename string
}

type PendingUploads struct {
	RecordsByJob       map[uint64][]*JobWithRecordInfoOutcome // job->[]unuploaded_records
	JobsByWARCFilename map[string][]uint64                    // warcFilename->[]job
	OutcomeByJob       map[uint64]string                      // job::outcome
	mu                 sync.Mutex
}

// if len(records) == 0: call the tracker to change the job status.
// else, defer the call until the warc of the job is uploaded (called by `OnWARCUploaded()`)
func (pu *PendingUploads) AddJob(job uint64, records []warc.RecordEvent, outcome string, tracker *bitclient.Client) {
	pu.mu.Lock()
	defer pu.mu.Unlock()

	if len(records) == 0 {
		if err := tracker.Move(context.TODO(), job, bitclient.MapWIP, outcome); err != nil {
			logger.Error("failed to move job (0 records, fast-path)", zap.Error(err), zap.Uint64("job", job), zap.String("outcome", outcome))
		}
		return
	}

	for _, record := range records {
		pu.RecordsByJob[job] = append(pu.RecordsByJob[job], &JobWithRecordInfoOutcome{
			uint64:       job,
			RecordInfo:   record.RecordInfo,
			WARCFilename: record.WARCFilename,
		})
		pu.JobsByWARCFilename[record.WARCFilename] = append(pu.JobsByWARCFilename[record.WARCFilename], job)
	}
	pu.OutcomeByJob[job] = outcome
}

func (pu *PendingUploads) OnWARCUploaded(warcFilename string, tracker *bitclient.Client) {
	pu.mu.Lock()
	defer pu.mu.Unlock()
	for _, job := range pu.JobsByWARCFilename[warcFilename] {
		outcome := pu.OutcomeByJob[job]
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
			// job 的全部 records 已经全部上传完成，可以发确认然后删除 job
			err := tracker.Move(context.TODO(), job, bitclient.MapWIP, outcome)
			if err != nil {
				logger.Error("failed to send job outcome (slow path)", zap.Int64("job", int64(job)), zap.Error(err))
			} else {
				logger.Info("job outcome sent (slow path)", zap.Int64("job", int64(job)), zap.String("outcome", outcome))
			}
			delete(pu.RecordsByJob, job)
			delete(pu.OutcomeByJob, job)
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
	for _, outcome := range pu.OutcomeByJob {
		metrics[outcome]++
	}
	return metrics
}

func UploadWARC(filepath, ARCHIVIST string) error {
	baseURL, _ := url.Parse("https://tus.saveweb.org/files")
	cl := tusgo.NewClient(http.DefaultClient, baseURL)

	f, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	metadata := make(map[string]string)
	metadata["filename"] = path.Base(f.Name())
	metadata["project"] = PROJECT
	metadata["archivist"] = ARCHIVIST

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
