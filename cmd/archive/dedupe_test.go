package main

import (
	"testing"
)

// 模拟三源共享 + 独占的真实分布（对照 info.md 2.4 的批量测试结果）。
func TestDedupeByETag(t *testing.T) {
	cands := []Candidate{
		// 同一文件，三源都命中 → ETag 一同，应合并为 1
		{URL: "s3.ivideo/x.flv", Ext: "flv", ETag: "AAA"},
		{URL: "edge.v.iask/x.flv", Ext: "flv", ETag: "AAA"},
		{URL: "edge.ivideo/x.flv", Ext: "flv", ETag: "AAA"},
		// 不同内容（不同 ext）→ 不同 ETag，各自保留
		{URL: "s3.ivideo/x.hlv", Ext: "hlv", ETag: "BBB"},
		// ipad 低清 mp4 → 又一个不同 ETag，保留
		{URL: "s3.ivideo/y.mp4", Ext: "mp4", ETag: "CCC"},
		// s3 独占文件（另两源 404 未进入候选）
		{URL: "s3.ivideo/z.flv", Ext: "flv", ETag: "DDD"},
		// 无 ETag 的两个 URL → 视为各自独立，都保留
		{URL: "s3.ivideo/w.flv", Ext: "flv", ETag: ""},
		{URL: "edge.v.iask/w.flv", Ext: "flv", ETag: ""},
	}

	got := dedupeByETag(cands)

	// 期望：AAA(1) + BBB(1) + CCC(1) + DDD(1) + 无ETag×2 = 6
	want := 6
	if len(got) != want {
		t.Fatalf("len = %d, want %d: %+v", len(got), want, got)
	}

	// AAA 应只出现一次，且是 sourceServers 排序最靠前的 s3.ivideo（去重保留首次出现）
	aaaCount := 0
	for _, c := range got {
		if c.ETag == "AAA" {
			aaaCount++
			if c.URL != "s3.ivideo/x.flv" {
				t.Errorf("AAA kept wrong url: %s", c.URL)
			}
		}
	}
	if aaaCount != 1 {
		t.Errorf("AAA count = %d, want 1", aaaCount)
	}
}
