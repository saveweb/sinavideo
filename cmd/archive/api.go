package main

import (
	"bytes"
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
	reqCtx := req.Context()
	reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

	r, err := client.Do(req.WithContext(reqCtx))
	if err != nil {
		return "", recordsEvents, err
	}
	defer func() {
		recordsEvents = <-feedbackCh
	}()
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
	VideoID, ok := data["video_id"].(string)
	if !ok {
		return "", recordsEvents, fmt.Errorf("video_id missing or not a string in vid resp: %v", data["video_id"])
	}
	return VideoID, recordsEvents, nil
}

// IpadVIDResp 对应 video_ids.php 的返回。
// 注意 ipad_vid 是混合类型：有低清 MP4 时是字符串，否则是 JSON false。
type IpadVIDResp struct {
	Vid     int             `json:"vid"`
	IpadVID json.RawMessage `json:"ipad_vid"`
}

// getIpadVID 通过 vid 查询对应的 ipad_vid（低清整段 MP4 的 ID）。
// 返回的 ipadVID 在「该视频没有转码低清版（ipad_vid 为 false）」时返回空字符串与 nil error。
// 视频时长 >6min 且被分段时，主 VID 拿不到原档，此时 ipad_vid 对应的低清 MP4 是 fallback 来源。
func getIpadVID(vid string) (ipadVID string, recordsEvents []warc.RecordEvent, err error) {
	url := "http://video.sina.com.cn/interface/video_ids/video_ids.php?v=" + vid
	log.Println("getIpadVID", url)
	req, _ := http.NewRequest("GET", url, nil)

	feedbackCh := make(chan warc.FeedbackEvent, 1)
	reqCtx := req.Context()
	reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

	r, err := client.Do(req.WithContext(reqCtx))
	if err != nil {
		return "", recordsEvents, err
	}
	defer func() {
		recordsEvents = <-feedbackCh
	}()
	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", recordsEvents, err
	}

	var resp IpadVIDResp
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		return "", recordsEvents, errors.Join(err, fmt.Errorf("unmarshal ipad_vid resp: %s", string(bodyBytes)))
	}

	// ipad_vid 为 false（未转码低清版）→ 返回空串，不是错误
	trimmed := bytes.TrimSpace(resp.IpadVID)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("false")) {
		return "", recordsEvents, nil
	}
	// 否则应当是字符串（可能带引号），剥掉引号
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", recordsEvents, fmt.Errorf("ipad_vid not a string: %s", string(trimmed))
	}
	return s, recordsEvents, nil
}

func getPlayInfo(videoID string) (playdata *PlayData, rawResp json.RawMessage, recordsEvents []warc.RecordEvent, err error) {
	url := "http://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id=" + videoID
	log.Println("getPlayInfo", url)

	req, _ := http.NewRequest("GET", url, nil)

	feedbackCh := make(chan warc.FeedbackEvent, 1)
	reqCtx := req.Context()
	reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

	r, err := client.Do(req.WithContext(reqCtx))
	if err != nil {
		return nil, nil, recordsEvents, err
	}
	defer func() {
		recordsEvents = <-feedbackCh
	}()
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
