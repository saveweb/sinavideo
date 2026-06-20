package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
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

func archive(vid string) error {
	log.Printf("=== VID %s ===", vid)

	videoID, err := getVideoID(vid)
	if err != nil {
		return err
	}
	log.Printf("video_id = %s", videoID)

	info, rawResp, err := getPlayInfo(videoID)
	dir := filepath.Join(flagOutput, vid)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	if rawResp != nil {
		os.WriteFile(filepath.Join(dir, "api_response.json"), rawResp, 0644)
	}

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
		u, ext, sz, ok := probeSRC(id)
		if !ok {
			log.Printf("  VID %s: not on source", id)
			continue
		}
		name := fmt.Sprintf("%s.%s", id, ext)
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			log.Printf("  %s: already exists, skip", name)
			meta.Files = append(meta.Files, FileRef{VID: id, Ext: ext, Size: sz, Filename: name})
			continue
		}
		log.Printf("  downloading %s (%d bytes)...", name, sz)
		if err := download(u, path); err != nil {
			log.Printf("  download %s failed: %v", name, err)
			continue
		}
		meta.Files = append(meta.Files, FileRef{VID: id, Ext: ext, Size: sz, Filename: name})
		log.Printf("  saved %s", name)
	}

	mj, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(dir, "metadata.json"), mj, 0644)

	log.Printf("=== VID %s done: %d files ===", vid, len(meta.Files))
	return nil
}
