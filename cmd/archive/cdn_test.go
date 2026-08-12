package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
)

func TestDownloadTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Length", "1024")
		response.WriteHeader(http.StatusOK)
		response.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()

	feedback := make(chan string, 2)
	settings := newWARCClientSettings("archive.test", 44, t.TempDir(), t.TempDir(), feedback)
	warcClient, err := warc.NewWARCWritingHTTPClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	client = warcClient
	logger = zap.NewNop()
	t.Cleanup(func() {
		client = nil
		if err := warcClient.Close(); err != nil {
			t.Error(err)
		}
		close(feedback)
	})

	_, err = downloadWithTimeout(t.Context(), server.URL, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("download error = %v, want context deadline exceeded", err)
	}
}
