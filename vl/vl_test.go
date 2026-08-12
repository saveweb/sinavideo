package vl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloseDrainsQueuedLogs(t *testing.T) {
	resetMetrics()
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	writer := NewVLWriter(server.URL, "", 10, 100, time.Hour)
	for _, line := range []string{"first\n", "second\n"} {
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()

	mu.Lock()
	got := strings.Join(bodies, "")
	mu.Unlock()
	if got != "first\nsecond\n" {
		t.Fatalf("sent body = %q", got)
	}
	if LogsEnqueued.Load() != 2 || LogsSent.Load() != 2 || LogsFailed.Load() != 0 || LogsDropped.Load() != 0 {
		t.Fatalf("metrics: enqueued=%d sent=%d failed=%d dropped=%d", LogsEnqueued.Load(), LogsSent.Load(), LogsFailed.Load(), LogsDropped.Load())
	}
}

func TestNon2xxResponseCountsFailedLogs(t *testing.T) {
	resetMetrics()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	writer := NewVLWriter(server.URL, "", 10, 1, time.Hour)
	writer.Write([]byte("line\n"))
	writer.Close()
	if LogsSent.Load() != 0 || LogsFailed.Load() != 1 {
		t.Fatalf("sent=%d failed=%d", LogsSent.Load(), LogsFailed.Load())
	}
}

func resetMetrics() {
	LogsEnqueued.Store(0)
	LogsDropped.Store(0)
	LogsSent.Store(0)
	LogsFailed.Store(0)
}
