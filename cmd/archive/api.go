package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
)

// var client = &http.Client{Timeout: 30 * time.Second}

type VidResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"` // ok=>map[string]any, err=>[]
}

type PlayResp struct {
	Message string          `json:"Message"`
	Code    int             `json:"code"`
	Data    json.RawMessage `json:"data"` // ok=>PlayData, err=>[]
}

type PlayData struct {
	Title      string      `json:"title"`
	CreateTime string      `json:"create_time"`
	Length     string      `json:"length"`
	Image      string      `json:"image"`
	Videos     []VideoFile `json:"videos"`
}

type VideoFile struct {
	FileID string `json:"file_id"`
	Type   string `json:"type"`
	Size   string `json:"size"`
}

// getvideoidbyvid
func getVideoID(vid string) (videoid string, recordsEvents []warc.RecordEvent, err error) {
	url := "https://s.video.sina.com.cn/video/getvideoidbyvid?vid=" + vid
	log.Println("getVideoID", url)
	req, _ := http.NewRequest("GET", url, nil)

	feedbackCh := make(chan warc.FeedbackEvent, 1)
	defer func() {
		recordsEvents = <-feedbackCh
	}()
	reqCtx := req.Context()
	reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

	r, err := client.Do(req.WithContext(reqCtx))
	if err != nil {
		return "", recordsEvents, err
	}
	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", recordsEvents, err
	}

	var v VidResp

	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		return "", recordsEvents, errors.Join(err, fmt.Errorf("unmarshal vid resp: %s", string(bodyBytes)))
	}
	if v.Code == 0 {
		return "", recordsEvents, fmt.Errorf("vid %s not found in api", vid)
	}
	if v.Code != 1 {
		return "", recordsEvents, fmt.Errorf("expected code 1, got %d, message: %s", v.Code, v.Message)
	}

	data, ok := v.Data.(map[string]any)
	if !ok {
		return "", recordsEvents, fmt.Errorf("unexpected data type: %T", v.Data)
	}
	VideoID := data["video_id"].(string)
	return VideoID, recordsEvents, nil
}

func getPlayInfo(videoID string) (playdata *PlayData, rawResp json.RawMessage, recordsEvents []warc.RecordEvent, err error) {
	url := "http://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id=" + videoID
	log.Println("getPlayInfo", url)

	req, _ := http.NewRequest("GET", url, nil)

	feedbackCh := make(chan warc.FeedbackEvent, 1)
	defer func() {
		recordsEvents = <-feedbackCh
	}()
	reqCtx := req.Context()
	reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

	r, err := client.Do(req.WithContext(reqCtx))
	if err != nil {
		return nil, nil, recordsEvents, err
	}
	defer r.Body.Close()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, recordsEvents, err
	}

	var p PlayResp
	if err = json.Unmarshal(raw, &p); err != nil {
		return
	}
	switch p.Code {
	case 0:
		return nil, raw, recordsEvents, fmt.Errorf("play api error, expected code 1, got %d, message: %s", p.Code, p.Message)
	case 1:
		var data PlayData
		if err := json.Unmarshal(p.Data, &data); err != nil {
			return nil, raw, recordsEvents, fmt.Errorf("unmarshal play data: %w", err)
		}
		return &data, raw, recordsEvents, nil
	default:
		return nil, raw, recordsEvents, fmt.Errorf("unexpected code: %d, message: %s", p.Code, p.Message)
	}
}
