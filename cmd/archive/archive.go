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
}

type Meta struct {
	VID        string    `json:"vid"`
	VideoID    string    `json:"video_id"`
	Title      string    `json:"title"`
	CreateTime string    `json:"create_time"`
	DurationMS int64     `json:"duration_ms"`
	Files      []FileRef `json:"files"`
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

	known := map[string]bool{vid: true}
	for _, f := range info.Videos {
		if f.FileID != "" {
			known[f.FileID] = true
		}
	}

	for id := range known {
		u, ext, sz, recs, ok := probeSRC(id)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if !ok {
			log.Printf("  VID %s: not on source", id)
			continue
		}
		name := fmt.Sprintf("%s.%s", id, ext)
		log.Printf("  downloading %s (%d bytes)...", name, sz)
		recs, err := download(u)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if err != nil {
			log.Printf("  download %s failed: %v", name, err)
			continue
		}
		meta.Files = append(meta.Files, FileRef{VID: id, Ext: ext, Size: sz, Filename: name})
		log.Printf("  saved %s", name)
	}

	log.Printf("=== VID %s done: %d files ===", vid, len(meta.Files))
	return allWarcRecEvents, nil
}
