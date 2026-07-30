package main

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cannerclient "github.com/saveweb/canner/client"
	warc "github.com/saveweb/gowarc"
	"github.com/saveweb/hq/pkg/protocol"
	"github.com/saveweb/unwarc"
	"go.uber.org/zap"
)

func TestJobWARCWriterProducesExactlyOneFile(t *testing.T) {
	outputDirectory := t.TempDir()
	tempDirectory := t.TempDir()
	feedback := make(chan string, 2)
	settings := newWARCClientSettings("archive.test", 42, outputDirectory, tempDirectory, feedback)

	if settings.RotatorSettings.WARCSize != math.MaxFloat64 {
		t.Fatalf("WARCSize = %v, want rotation disabled", settings.RotatorSettings.WARCSize)
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
	if err := warcClient.Close(); err != nil {
		t.Fatal(err)
	}
	close(feedback)

	var filenames []string
	for filename := range feedback {
		filenames = append(filenames, filename)
	}
	if len(filenames) != 1 {
		t.Fatalf("got %d WARC files, want one: %v", len(filenames), filenames)
	}
	if !strings.HasPrefix(filenames[0], "SINA_VIDEO_42-") || !strings.HasSuffix(filenames[0], ".warc.zst") {
		t.Fatalf("unexpected WARC filename %q", filenames[0])
	}
	warcPath := filepath.Join(outputDirectory, filenames[0])
	if _, err := os.Stat(warcPath); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(warcPath)
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
	outputDirectory := t.TempDir()
	tempDirectory := t.TempDir()
	feedback := make(chan string, 2)
	settings := newWARCClientSettings("archive.test", 43, outputDirectory, tempDirectory, feedback)

	warcClient, err := warc.NewWARCWritingHTTPClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	client = warcClient
	t.Cleanup(func() {
		client = nil
		if err := warcClient.Close(); err != nil {
			t.Error(err)
		}
		close(feedback)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := archive(ctx, "60233854"); !errors.Is(err, context.Canceled) {
		t.Fatalf("archive() error = %v, want context.Canceled", err)
	}
}

func TestArtifactReceipt(t *testing.T) {
	want := protocol.ArtifactReceipt{
		ID:         "receipt-1",
		Issuer:     "https://canner.example",
		ObjectID:   "object-1",
		Checksum:   "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes:  1234,
		AcceptedAt: 1785031200,
	}
	got := artifactReceipt(cannerclient.Receipt{
		ID:         want.ID,
		Issuer:     want.Issuer,
		ObjectID:   want.ObjectID,
		Checksum:   want.Checksum,
		SizeBytes:  want.SizeBytes,
		AcceptedAt: want.AcceptedAt,
	})
	if got != want {
		t.Fatalf("artifactReceipt() = %+v, want %+v", got, want)
	}
}

func TestLiveArchiveAndCannerUpload(t *testing.T) {
	vid := os.Getenv("SINAVIDEO_LIVE_VID")
	cannerURL := os.Getenv("SINAVIDEO_TEST_CANNER_URL")
	if vid == "" || cannerURL == "" {
		t.Skip("set SINAVIDEO_LIVE_VID and SINAVIDEO_TEST_CANNER_URL to run the live archive test")
	}
	logger = zap.NewNop()

	warcPath, archiveErr, err := archiveJobTo(t.Context(), 9000001, vid, "test-archivist", "archive.test", t.TempDir(), t.TempDir())
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
	receipt, err := canner.UploadFile(t.Context(), HQProject, warcPath)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SizeBytes != info.Size() {
		t.Fatalf("receipt size = %d, WARC size = %d", receipt.SizeBytes, info.Size())
	}
	if !strings.HasPrefix(receipt.Checksum, "blake3:") {
		t.Fatalf("receipt checksum = %q, want blake3 checksum", receipt.Checksum)
	}
}
