package archive

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cannerclient "github.com/saveweb/canner/client"
	warc "github.com/saveweb/gowarc"
	"github.com/saveweb/hq/pkg/protocol"
	"github.com/saveweb/unwarc"
	"go.uber.org/zap"
)

func TestJobWARCWriterProducesExactlyOneFile(t *testing.T) {
	outputDirectory := t.TempDir()
	settings := newWARCClientSettings("archive.test", 42, outputDirectory, t.TempDir())

	if settings.RotatorSettings.WARCSize != math.MaxFloat64 {
		t.Fatalf("WARCSize = %v, want rotation disabled", settings.RotatorSettings.WARCSize)
	}
	if settings.DisableKeepAlives {
		t.Fatal("keepalive is disabled")
	}
	if settings.ConnReadDeadline != 30*time.Second {
		t.Fatalf("ConnReadDeadline = %v, want 30s", settings.ConnReadDeadline)
	}
	if got := settings.RotatorSettings.WarcinfoContent.Get("hostname"); got != "archive.test" {
		t.Fatalf("hostname = %q, want archive.test", got)
	}

	warcClient, err := warc.NewWARCWritingHTTPClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJobMetadata(warcClient, "60233854", "archivist", string(protocol.OutcomeSuccess), nil); err != nil {
		t.Fatal(err)
	}
	finalized, err := warcClient.Shutdown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(finalized.FinalizedFiles) != 1 {
		t.Fatalf("finalized files = %v, want one", finalized.FinalizedFiles)
	}
	filename := finalized.FinalizedFiles[0]
	if !strings.HasPrefix(filename, "SINA_VIDEO_42-") || !strings.HasSuffix(filename, ".warc.zst") {
		t.Fatalf("unexpected WARC filename %q", filename)
	}

	file, err := os.Open(filepath.Join(outputDirectory, filename))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner, err := unwarc.NewScanner(file, unwarc.DefaultScannerOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()
	records := 0
	for scanner.Next() {
		records++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if records != 2 {
		t.Fatalf("strict scan found %d records, want warcinfo and metadata", records)
	}
}

func TestArchiveHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	archiver := New(zap.NewNop(), t.TempDir(), t.TempDir())
	_, archiveErr, err := archiver.ArchiveJob(ctx, 43, "60233854", "archivist", "archive.test")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(archiveErr, context.Canceled) {
		t.Fatalf("archive error = %v, want context.Canceled", archiveErr)
	}
}

func TestLiveArchiveAndCannerUpload(t *testing.T) {
	vid := os.Getenv("SINAVIDEO_LIVE_VID")
	cannerURL := os.Getenv("SINAVIDEO_TEST_CANNER_URL")
	if vid == "" || cannerURL == "" {
		t.Skip("set SINAVIDEO_LIVE_VID and SINAVIDEO_TEST_CANNER_URL to run the live archive test")
	}
	archiver := New(zap.NewNop(), t.TempDir(), t.TempDir())
	warcPath, archiveErr, err := archiver.ArchiveJob(t.Context(), 9000001, vid, "test-archivist", "archive.test")
	if err != nil {
		t.Fatal(err)
	}
	if archiveErr != nil {
		t.Fatal(archiveErr)
	}
	info, err := os.Stat(warcPath)
	if err != nil {
		t.Fatal(err)
	}
	canner, err := cannerclient.New(cannerURL)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := canner.UploadFile(t.Context(), Project, warcPath)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SizeBytes != info.Size() || !strings.HasPrefix(receipt.Checksum, "blake3:") {
		t.Fatalf("receipt does not match WARC: %+v", receipt)
	}
}
