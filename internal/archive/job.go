package archive

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"time"

	warc "github.com/saveweb/gowarc"
	"github.com/saveweb/hq/pkg/protocol"
	"go.uber.org/zap"
)

const (
	Project                = "sinavideo"
	DefaultOutputDirectory = "warcs"
	DefaultTempDirectory   = "temp"
	userAgent              = "Mozilla/5.0 (compatible; saveweb) sinavideo_archive/020260620"
)

type Archiver struct {
	logger          *zap.Logger
	outputDirectory string
	tempDirectory   string
	client          *warc.CustomHTTPClient
}

func New(logger *zap.Logger, outputDirectory, tempDirectory string) *Archiver {
	return &Archiver{logger: logger, outputDirectory: outputDirectory, tempDirectory: tempDirectory}
}

func (a *Archiver) ArchiveJob(ctx context.Context, jobID int64, vid, userID, hostname string) (warcPath string, archiveErr, err error) {
	settings := newWARCClientSettings(hostname, jobID, a.outputDirectory, a.tempDirectory)

	warcClient, err := warc.NewWARCWritingHTTPClient(settings)
	if err != nil {
		return "", nil, err
	}
	a.client = warcClient
	defer func() { a.client = nil }()

	records, archiveErr := a.archive(ctx, vid)
	outcome := string(protocol.OutcomeSuccess)
	if archiveErr != nil {
		outcome = "failed"
	}
	metadataErr := writeJobMetadata(warcClient, vid, userID, outcome, records)
	// Network work obeys ctx, but a canceled lease must not leave an open WARC.
	finalizeResult, finalizeErr := warcClient.Shutdown(context.Background())
	if err := errors.Join(metadataErr, finalizeErr); err != nil {
		return "", archiveErr, err
	}

	filenames := finalizeResult.FinalizedFiles
	if len(filenames) != 1 {
		return "", archiveErr, fmt.Errorf("job %d produced %d WARC files, want exactly one", jobID, len(filenames))
	}
	return filepath.Join(a.outputDirectory, filenames[0]), archiveErr, nil
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

func writeJobMetadata(warcClient *warc.CustomHTTPClient, vid, userID, outcome string, records []warc.RecordEvent) error {
	metadataRecord := warc.NewRecord(warcClient.TempDir)
	metadataRecord.Header.Set("WARC-Type", "metadata")
	metadataRecord.Header.Set("WARC-Target-URI", "urn:saveweb:"+Project+":"+vid)
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
	result, err := warcClient.WriteBatch(context.Background(), recordBatch)
	if err != nil {
		return err
	}
	if len(result.Events) != 1 {
		return fmt.Errorf("metadata writer returned %d records, want one", len(result.Events))
	}
	return nil
}
