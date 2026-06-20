package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
func getVideoID(vid string) (string, error) {
	url := "https://s.video.sina.com.cn/video/getvideoidbyvid?vid=" + vid
	log.Println("getVideoID", url)
	r, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	var v VidResp
	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		return "", errors.Join(err, fmt.Errorf("unmarshal vid resp: %s", string(bodyBytes)))
	}
	if v.Code == 0 {
		return "", fmt.Errorf("vid %s not found in api", vid)
	}
	if v.Code != 1 {
		return "", fmt.Errorf("expected code 1, got %d, message: %s", v.Code, v.Message)
	}

	data, ok := v.Data.(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected data type: %T", v.Data)
	}
	VideoID := data["video_id"].(string)
	return VideoID, nil
}

func getPlayInfo(videoID string) (*PlayData, json.RawMessage, error) {
	url := "http://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id=" + videoID
	log.Println("getPlayInfo", url)
	r, err := client.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer r.Body.Close()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, err
	}

	var p PlayResp
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil, err
	}
	switch p.Code {
	case 0:
		return nil, raw, fmt.Errorf("play api error, expected code 1, got %d, message: %s", p.Code, p.Message)
	case 1:
		var data PlayData
		if err := json.Unmarshal(p.Data, &data); err != nil {
			return nil, raw, fmt.Errorf("unmarshal play data: %w", err)
		}
		return &data, raw, nil
	default:
		return nil, raw, fmt.Errorf("unexpected code: %d, message: %s", p.Code, p.Message)
	}
}
