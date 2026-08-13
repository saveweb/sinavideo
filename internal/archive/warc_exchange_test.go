package archive

import (
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
	"github.com/saveweb/unwarc"
	"go.uber.org/zap"
)

func TestDownloadNonOKArchivesCompleteExchange(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		stdhttp.Error(w, "missing", stdhttp.StatusNotFound)
	}))
	defer server.Close()
	warcClient, outputDirectory := installTestWARCClient(t)

	events, err := newTestArchiver(t, warcClient).downloadWithTimeout(t.Context(), server.URL, time.Second)
	if err == nil || !strings.Contains(err.Error(), "http 404") {
		t.Fatalf("download error = %v, want HTTP 404", err)
	}
	assertHTTPRecordEvents(t, events, false)
	assertStrictFinalizedWARC(t, warcClient, outputDirectory, 3)
}

func TestDownloadTimeoutArchivesTruncatedExchange(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 32))
		w.(stdhttp.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	warcClient, outputDirectory := installTestWARCClient(t)

	events, err := newTestArchiver(t, warcClient).downloadWithTimeout(t.Context(), server.URL, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("download error = %v, want context.DeadlineExceeded", err)
	}
	assertHTTPRecordEvents(t, events, true)
	assertStrictFinalizedWARC(t, warcClient, outputDirectory, 3)
}

func TestRequestCancellationArchivesTruncatedExchange(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 32))
		w.(stdhttp.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	warcClient, outputDirectory := installTestWARCClient(t)

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, events, err := newTestArchiver(t, warcClient).executeWARCRequest(req, func(response *http.Response) error {
		buf := make([]byte, 32)
		if _, err := io.ReadFull(response.Body, buf); err != nil {
			return err
		}
		cancel()
		_, err := io.Copy(io.Discard, response.Body)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v, want context.Canceled", err)
	}
	assertHTTPRecordEvents(t, events, true)
	assertStrictFinalizedWARC(t, warcClient, outputDirectory, 3)
}

func TestReadWARCURLTimeout(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(stdhttp.StatusOK)
		w.(stdhttp.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	warcClient, outputDirectory := installTestWARCClient(t)

	_, events, err := newTestArchiver(t, warcClient).readWARCURLWithTimeout(t.Context(), server.URL, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readWARCURL error = %v, want context.DeadlineExceeded", err)
	}
	assertHTTPRecordEvents(t, events, true)
	assertStrictFinalizedWARC(t, warcClient, outputDirectory, 3)
}

func installTestWARCClient(t *testing.T) (*warc.CustomHTTPClient, string) {
	t.Helper()
	outputDirectory := t.TempDir()
	settings := warc.HTTPClientSettings{
		RotatorSettings: warc.NewRotatorSettings("archive.test"),
		TempDir:         t.TempDir(),
		DigestAlgorithm: warc.BLAKE3,
	}
	settings.RotatorSettings.OutputDirectory = outputDirectory
	warcClient, err := warc.NewWARCWritingHTTPClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	return warcClient, outputDirectory
}

func newTestArchiver(t *testing.T, client *warc.CustomHTTPClient) *Archiver {
	t.Helper()
	return &Archiver{client: client, logger: zap.NewNop()}
}

func assertHTTPRecordEvents(t *testing.T, events []warc.RecordEvent, truncated bool) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("record events = %d, want request and response", len(events))
	}
	types := make(map[string]warc.RecordEvent, len(events))
	for _, event := range events {
		types[event.RecordInfo.Header.Get("WARC-Type")] = event
	}
	if _, ok := types["request"]; !ok {
		t.Fatal("request record event is missing")
	}
	response, ok := types["response"]
	if !ok {
		t.Fatal("response record event is missing")
	}
	if got := response.RecordInfo.Header.Get("WARC-Truncated"); truncated && got == "" {
		t.Fatal("canceled response is not marked truncated")
	} else if !truncated && got != "" {
		t.Fatalf("complete response WARC-Truncated = %q", got)
	}
}

func assertStrictFinalizedWARC(t *testing.T, warcClient *warc.CustomHTTPClient, outputDirectory string, wantRecords int) {
	t.Helper()
	finalized, err := warcClient.Shutdown(context.Background())
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(finalized.FinalizedFiles) != 1 {
		t.Fatalf("finalized files = %v, want one", finalized.FinalizedFiles)
	}
	path := filepath.Join(outputDirectory, finalized.FinalizedFiles[0])
	file, err := os.Open(path)
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
	var recordTypes []string
	for scanner.Next() {
		records++
		recordType, _ := scanner.RecordRef().Header.Get("WARC-Type")
		recordTypes = append(recordTypes, recordType)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if records != wantRecords {
		t.Fatalf("strict scan found %d records, want %d", records, wantRecords)
	}
	if want := []string{"warcinfo", "request", "response"}; !slices.Equal(recordTypes, want) {
		t.Fatalf("WARC record order = %v, want %v", recordTypes, want)
	}
}
