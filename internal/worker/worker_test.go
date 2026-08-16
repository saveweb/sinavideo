package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	cannerclient "github.com/saveweb/canner/client"
	"github.com/saveweb/hq/pkg/protocol"
	"go.uber.org/zap"
)

type testStatusError struct {
	code int
}

func (e *testStatusError) Error() string   { return fmt.Sprintf("http %d", e.code) }
func (e *testStatusError) StatusCode() int { return e.code }

func TestArtifactReceipt(t *testing.T) {
	want := protocol.ArtifactReceipt{
		ID: "receipt-1", Issuer: "https://canner.example", ObjectID: "object-1",
		Checksum:  "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 1234, AcceptedAt: 1785031200,
	}
	got := artifactReceipt(cannerclient.Receipt{
		ID: want.ID, Issuer: want.Issuer, ObjectID: want.ObjectID,
		Checksum: want.Checksum, SizeBytes: want.SizeBytes, AcceptedAt: want.AcceptedAt,
	})
	if got != want {
		t.Fatalf("artifactReceipt() = %+v, want %+v", got, want)
	}
}

func TestRemoveJobWARC(t *testing.T) {
	warcPath := filepath.Join(t.TempDir(), "job.warc.zst")
	if err := os.WriteFile(warcPath, []byte("warc"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeJobWARC(warcPath, 42, zap.NewNop())
	if _, err := os.Stat(warcPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WARC still exists after cleanup: %v", err)
	}
	removeJobWARC(warcPath, 42, zap.NewNop())
}

func TestRetryableFailureIncludesStatusCode(t *testing.T) {
	failure := retryableFailure("archive_failed", fmt.Errorf("get video id: %w", &testStatusError{code: 418}))
	if got := failure.Error.Details["status_code"]; got != 418 {
		t.Fatalf("status_code = %v, want 418", got)
	}
}

func TestUploadJobWARCWithRetryEventuallySucceeds(t *testing.T) {
	want := cannerclient.Receipt{ID: "receipt-1"}
	attempts := 0
	got, err := uploadJobWARCWithRetry(t.Context(), func(context.Context) (cannerclient.Receipt, error) {
		attempts++
		if attempts < 3 {
			return cannerclient.Receipt{}, errors.New("temporary upload failure")
		}
		return want, nil
	}, func(int) time.Duration { return 0 }, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got != want || attempts != 3 {
		t.Fatalf("receipt = %+v, attempts = %d; want %+v after 3 attempts", got, attempts, want)
	}
}

func TestUploadJobWARCWithRetryStopsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	wantErr := errors.New("lease lost")
	uploaded := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := uploadJobWARCWithRetry(ctx, func(context.Context) (cannerclient.Receipt, error) {
			close(uploaded)
			return cannerclient.Receipt{}, errors.New("temporary upload failure")
		}, func(int) time.Duration { return time.Hour }, zap.NewNop())
		done <- err
	}()
	<-uploaded
	cancel(wantErr)
	if err := <-done; !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestUploadRetryBackoff(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, time.Minute, time.Minute}
	attempts := []int{1, 2, 3, 7, 100}
	for i, attempt := range attempts {
		if got := uploadRetryBackoff(attempt); got != want[i] {
			t.Fatalf("uploadRetryBackoff(%d) = %s, want %s", attempt, got, want[i])
		}
	}
}
