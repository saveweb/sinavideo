package main

import (
	"fmt"
	"log"
	"strconv"

	warc "github.com/saveweb/gowarc"
)

type FileRef struct {
	VID      string `json:"vid"`
	Ext      string `json:"ext"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
	// Source 标记该文件的来源："main" 为主档高清（vid/file_id），
	// "ipad" 为 ipad_vid 低清整段 MP4（来自 video_ids.php）。
	Source string `json:"source"`
}

type Meta struct {
	VID        string    `json:"vid"`
	VideoID    string    `json:"video_id"`
	Title      string    `json:"title"`
	CreateTime string    `json:"create_time"`
	DurationMS int64     `json:"duration_ms"`
	Files      []FileRef `json:"files"`
}

// taggedCandidate 给候选下载项挂上来源标记（main / ipad）与对应的 id，
// 便于下载成功后回填 meta.Files。
type taggedCandidate struct {
	Candidate
	Source string
	ID     string
}

func archive(vid string) (allWarcRecEvents []warc.RecordEvent, err error) {
	log.Printf("=== VID %s ===", vid)

	videoID, recordsIDs, err := getVideoID(vid)
	if err != nil {
		return allWarcRecEvents, err
	}
	allWarcRecEvents = append(allWarcRecEvents, recordsIDs...)

	log.Printf("video_id = %s", videoID)

	info, _, recordsIDs, err := getPlayInfo(videoID)
	allWarcRecEvents = append(allWarcRecEvents, recordsIDs...)

	if err != nil {
		log.Printf("  play api failed: %v, trying sources directly", err)
		info = &PlayData{}
	} else {
		log.Printf("  title = %q  length = %s", info.Title, info.Length)
	}

	meta := Meta{
		VID:        vid,
		VideoID:    videoID,
		Title:      info.Title,
		CreateTime: info.CreateTime,
	}
	if d, err := strconv.ParseInt(info.Length, 10, 64); err == nil {
		meta.DurationMS = d
	}

	// 收集所有要探测的 id：主 vid + play API 返回的分段 file_id
	known := map[string]bool{vid: true}
	for _, f := range info.Videos {
		if f.FileID != "" {
			known[f.FileID] = true
		}
	}

	// 对每个 id 探测「所有源 × 全扩展名」，收集全部 200 命中的候选。
	var cands []taggedCandidate
	for id := range known {
		cs, recs := probeCandidates(id, exts)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		for _, c := range cs {
			cands = append(cands, taggedCandidate{Candidate: c, Source: "main", ID: id})
		}
	}

	// ipad_vid 低清整段 MP4 通道：作为 >=6min 分段视频的兜底来源，
	// 也是不同质量版本，与主档一并存档。只探测 .mp4（低清 MP4 的固定格式）。
	if ipadVID, recs, ipadErr := getIpadVID(vid); ipadErr != nil {
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		log.Printf("  ipad_vid lookup failed: %v", ipadErr)
	} else {
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if ipadVID != "" && ipadVID != vid && !known[ipadVID] {
			cs, recs := probeCandidates(ipadVID, []string{"mp4"})
			allWarcRecEvents = append(allWarcRecEvents, recs...)
			for _, c := range cs {
				cands = append(cands, taggedCandidate{Candidate: c, Source: "ipad", ID: ipadVID})
			}
		}
	}

	// ETag 去重：三个源共享大量数据，同一文件常能通过多个 URL 访问且 ETag 一致；
	// 按 ETag 去重后在最大化覆盖率的同时避免重复下载同一字节。
	// 注意 main 与 ipad 的 ETag 必然不同（不同质量版本），不会被互相去重。
	uniq := dedupeByETag(untag(cands))
	want := map[string]taggedCandidate{} // url -> tag
	for _, c := range uniq {
		for _, t := range cands {
			if t.URL == c.URL {
				want[t.URL] = t
				break
			}
		}
	}

	for u, t := range want {
		name := fmt.Sprintf("%s.%s", t.ID, t.Ext)
		log.Printf("  downloading %s (%d bytes)...", name, t.Size)
		recs, derr := download(u)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if derr != nil {
			log.Printf("  download %s failed: %v", name, derr)
			continue
		}
		meta.Files = append(meta.Files, FileRef{VID: t.ID, Ext: t.Ext, Size: t.Size, Filename: name, Source: t.Source})
		log.Printf("  saved %s", name)
	}

	log.Printf("=== VID %s done: %d files ===", vid, len(meta.Files))
	return allWarcRecEvents, nil
}

// untag 仅用于给 dedupeByETag 喂数据，保留指针关联。
func untag(in []taggedCandidate) []Candidate {
	out := make([]Candidate, len(in))
	for i, c := range in {
		out[i] = c.Candidate
	}
	return out
}

