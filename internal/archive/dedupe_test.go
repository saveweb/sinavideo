package archive

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestGroupCandidatesByETagKeepsFallbacks(t *testing.T) {
	core, observed := observer.New(zap.InfoLevel)
	a := &Archiver{logger: zap.New(core)}
	candidates := []taggedCandidate{
		{Candidate: Candidate{URL: "http://primary/video.mp4", ETag: "same"}, ID: "42", Source: "main"},
		{Candidate: Candidate{URL: "http://mirror/video.mp4", ETag: "same"}, ID: "42", Source: "main"},
		{Candidate: Candidate{URL: "http://other/video.mp4"}, ID: "43", Source: "ipad"},
	}

	groups := a.groupCandidatesByETag(candidates)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Fatalf("fallbacks = %d, want 2", len(groups[0]))
	}
	if groups[0][0].URL != candidates[0].URL || groups[0][1].URL != candidates[1].URL {
		t.Fatalf("fallback order = %v, want primary then mirror", groups[0])
	}
	entries := observed.FilterMessage("dedupe fallback").All()
	if len(entries) != 1 {
		t.Fatalf("dedupe fallback logs = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["url"] != candidates[1].URL || fields["primary_url"] != candidates[0].URL || fields["etag"] != "same" {
		t.Fatalf("dedupe fallback fields = %#v", fields)
	}
}

func TestDownloadRetryPolicy(t *testing.T) {
	if !shouldRetryDownload(&httpStatusError{code: 404}) {
		t.Fatal("HTTP 404 should be retried locally")
	}
	if !shouldRetryDownload(&httpStatusError{code: 502}) {
		t.Fatal("HTTP 502 should be retried locally")
	}
	if shouldRetryDownload(&httpStatusError{code: 418}) {
		t.Fatal("HTTP 418 should fail the job without local retry")
	}
}
