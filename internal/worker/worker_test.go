package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
