package archive

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	warc "github.com/saveweb/gowarc"
	"github.com/saveweb/hq/pkg/protocol"
	"github.com/saveweb/unwarc"
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
