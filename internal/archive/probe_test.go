package archive

import (
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeRetriesTransientStatus(t *testing.T) {
	oldServers := sourceServers
	oldBackoff := mediaDownloadBackoff
	mediaDownloadBackoff = [...]time.Duration{0, 0}
	t.Cleanup(func() {
		sourceServers = oldServers
		mediaDownloadBackoff = oldBackoff
	})

	var requests atomic.Int32
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		if requests.Add(1) < 3 {
			stdhttp.Error(w, "temporary", stdhttp.StatusBadGateway)
			return
		}
		w.Header().Set("ETag", "same")
		w.Header().Set("Content-Length", "123")
		w.WriteHeader(stdhttp.StatusOK)
	}))
	defer server.Close()
	sourceServers = []string{server.URL + "/%s.%s"}
	warcClient, outputDirectory := installTestWARCClient(t)

	candidates, events, err := newTestArchiver(t, warcClient).probeCandidates(t.Context(), "42", []string{"mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
	if len(candidates) != 1 || candidates[0].ETag != "same" || candidates[0].Size != 123 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if len(events) != 6 {
		t.Fatalf("record events = %d, want three request/response pairs", len(events))
	}
	assertStrictFinalizedWARC(t, warcClient, outputDirectory, 7)
}

func TestProbeDoesNotRetryTeapot(t *testing.T) {
	oldServers := sourceServers
	t.Cleanup(func() { sourceServers = oldServers })

	var requests atomic.Int32
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		requests.Add(1)
		stdhttp.Error(w, "teapot", stdhttp.StatusTeapot)
	}))
	defer server.Close()
	sourceServers = []string{server.URL + "/%s.%s"}
	warcClient, outputDirectory := installTestWARCClient(t)

	_, events, err := newTestArchiver(t, warcClient).probeCandidates(t.Context(), "42", []string{"mp4"})
	if err == nil || !strings.Contains(err.Error(), "http 418") {
		t.Fatalf("probe error = %v, want HTTP 418", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	assertHTTPRecordEvents(t, events, false)
	assertStrictFinalizedWARC(t, warcClient, outputDirectory, 3)
}
