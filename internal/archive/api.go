package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
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
func (a *Archiver) getVideoID(ctx context.Context, vid string) (string, []warc.RecordEvent, error) {
	url := "https://s.video.sina.com.cn/video/getvideoidbyvid?vid=" + vid
	a.logger.Info("getVideoID", zap.String("url", url))
	bodyBytes, recordsEvents, err := a.readWARCURL(ctx, url)
	if err != nil {
		return "", recordsEvents, err
	}

	var v VidResp

	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		return "", recordsEvents, errors.Join(err, fmt.Errorf("unmarshal vid resp: %s", string(bodyBytes)))
	}
	if v.Code == 0 {
		return "", recordsEvents, &sourceUnavailableError{source: "video ID", message: fmt.Sprintf("vid %s not found in API", vid)}
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

type WAPVideoInfoResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type WAPVideoInfo struct {
	MP4VID string `json:"mp4vid"`
	Image  string `json:"image"`
}

type sourceUnavailableError struct {
	source  string
	message string
}

func (e *sourceUnavailableError) Error() string {
	return e.source + " unavailable: " + e.message
}

func isSourceUnavailable(err error) bool {
	var unavailable *sourceUnavailableError
	return errors.As(err, &unavailable)
}

// getIpadVID 通过 vid 查询对应的 ipad_vid（低清整段 MP4 的 ID）。
// 返回的 ipadVID 在「该视频没有转码低清版（ipad_vid 为 false）」时返回空字符串与 nil error。
// 视频时长 >6min 且被分段时，主 VID 拿不到原档，此时 ipad_vid 对应的低清 MP4 是 fallback 来源。
func (a *Archiver) getIpadVID(ctx context.Context, vid string) (string, []warc.RecordEvent, error) {
	url := "https://video.sina.com.cn/interface/video_ids/video_ids.php?v=" + vid
	a.logger.Info("getIpadVID", zap.String("url", url))
	bodyBytes, recordsEvents, err := a.readWARCURL(ctx, url)
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

func parseWAPVideoInfo(raw []byte) (WAPVideoInfo, error) {
	var resp WAPVideoInfoResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return WAPVideoInfo{}, errors.Join(err, fmt.Errorf("unmarshal WAP video info resp: %s", string(raw)))
	}
	if resp.Code != 1 {
		return WAPVideoInfo{}, &sourceUnavailableError{source: "WAP video info", message: fmt.Sprintf("expected code 1, got %d, message: %s", resp.Code, resp.Message)}
	}

	var info WAPVideoInfo
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return WAPVideoInfo{}, errors.Join(err, fmt.Errorf("unmarshal WAP video info data: %s", string(resp.Data)))
	}
	return info, nil
}

// getWAPVideoInfo queries the legacy WAP metadata endpoint. Some deleted videos
// expose a playable MP4 and thumbnail only here even when the play API fails.
func (a *Archiver) getWAPVideoInfo(ctx context.Context, vid string) (WAPVideoInfo, []warc.RecordEvent, error) {
	url := "https://interface.sina.cn/video/wap/videoinfo.d.json?vid=" + vid
	a.logger.Info("getWAPVideoInfo", zap.String("url", url))
	raw, recordsEvents, err := a.readWARCURL(ctx, url)
	if err != nil {
		return WAPVideoInfo{}, recordsEvents, err
	}
	info, err := parseWAPVideoInfo(raw)
	return info, recordsEvents, err
}

func (a *Archiver) getPlayInfo(ctx context.Context, videoID string) (*PlayData, json.RawMessage, []warc.RecordEvent, error) {
	url := "https://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id=" + videoID
	a.logger.Info("getPlayInfo", zap.String("url", url))

	raw, recordsEvents, err := a.readWARCURL(ctx, url)
	if err != nil {
		return nil, nil, recordsEvents, err
	}

	var p PlayResp
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil, recordsEvents, err
	}
	switch p.Code {
	case 0:
		return nil, raw, recordsEvents, &sourceUnavailableError{source: "play API", message: fmt.Sprintf("expected code 1, got %d, message: %s", p.Code, p.Message)}
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
