package archive

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
)

type FileRef struct {
	VID      string `json:"vid"`
	Ext      string `json:"ext"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
	// Source 标记该文件的来源："main" 为主档高清（vid/file_id），
	// "ipad" 和 "wap" 为对应 API 发现的低清整段 MP4。
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

// taggedCandidate 给候选下载项挂上来源标记（main / ipad / wap）与对应的 id，
// 便于下载成功后回填 meta.Files。
type taggedCandidate struct {
	Candidate
	Source string
	ID     string
}

func (a *Archiver) archive(ctx context.Context, vid string) (allWarcRecEvents []warc.RecordEvent, err error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	a.logger.Info("started VID archive")

	videoID, recordsIDs, err := a.getVideoID(ctx, vid)
	allWarcRecEvents = append(allWarcRecEvents, recordsIDs...)
	if err != nil {
		if ctx.Err() != nil {
			return allWarcRecEvents, err
		}
		if !isSourceUnavailable(err) && !isHTTPStatus(err, 404) {
			return allWarcRecEvents, err
		}
		a.logger.Info("video ID unavailable; continuing with VID-based sources", zap.Error(err))
	} else {
		a.logger.Info("resolved video ID", zap.String("video_id", videoID))
	}

	info := &PlayData{}
	if videoID != "" {
		playInfo, _, playRecords, playErr := a.getPlayInfo(ctx, videoID)
		allWarcRecEvents = append(allWarcRecEvents, playRecords...)
		if playErr != nil {
			if ctx.Err() != nil {
				return allWarcRecEvents, playErr
			}
			if !isSourceUnavailable(playErr) && !isHTTPStatus(playErr, 400, 404) {
				return allWarcRecEvents, playErr
			}
			a.logger.Info("play API unavailable; trying sources directly", zap.Error(playErr))
		} else {
			info = playInfo
			a.logger.Info("loaded play metadata", zap.String("title", info.Title), zap.String("length", info.Length))
		}
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

	var imageURLs []string
	seenImageURLs := map[string]bool{}
	addImageURL := func(u string) {
		if u != "" && !seenImageURLs[u] {
			seenImageURLs[u] = true
			imageURLs = append(imageURLs, u)
		}
	}
	addImageURL(info.Image)

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
		cs, recs, err := a.probeCandidates(ctx, id, exts)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if err != nil {
			return allWarcRecEvents, err
		}
		for _, c := range cs {
			cands = append(cands, taggedCandidate{Candidate: c, Source: "main", ID: id})
		}
	}

	// ipad_vid 低清整段 MP4 通道：作为 >=6min 分段视频的兜底来源，
	// 也是不同质量版本，与主档一并存档。只探测 .mp4（低清 MP4 的固定格式）。
	if ipadVID, recs, ipadErr := a.getIpadVID(ctx, vid); ipadErr != nil {
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if ctx.Err() != nil {
			return allWarcRecEvents, ipadErr
		}
		if !isHTTPStatus(ipadErr, 404) {
			return allWarcRecEvents, ipadErr
		}
		a.logger.Info("ipad_vid unavailable", zap.Error(ipadErr))
	} else {
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if ipadVID != "" && ipadVID != vid && !known[ipadVID] {
			cs, recs, err := a.probeCandidates(ctx, ipadVID, []string{"mp4"})
			allWarcRecEvents = append(allWarcRecEvents, recs...)
			if err != nil {
				return allWarcRecEvents, err
			}
			for _, c := range cs {
				cands = append(cands, taggedCandidate{Candidate: c, Source: "ipad", ID: ipadVID})
			}
			known[ipadVID] = true
		}
	}

	// 失效视频的 WAP 元数据有时仍保留 mp4vid，即使 video_ids.php 返回
	// ipad_vid=false。该 ID 对应 s3.ivideo.sina.com.cn 上的整段 MP4。
	if wapInfo, recs, wapErr := a.getWAPVideoInfo(ctx, vid); wapErr != nil {
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if ctx.Err() != nil {
			return allWarcRecEvents, wapErr
		}
		if !isSourceUnavailable(wapErr) && !isHTTPStatus(wapErr, 404) {
			return allWarcRecEvents, wapErr
		}
		a.logger.Info("WAP video info unavailable", zap.Error(wapErr))
	} else {
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		mp4VID := wapInfo.MP4VID
		if mp4VID != "" && !known[mp4VID] {
			cs, recs, err := a.probeCandidates(ctx, mp4VID, []string{"mp4"})
			allWarcRecEvents = append(allWarcRecEvents, recs...)
			if err != nil {
				return allWarcRecEvents, err
			}
			for _, c := range cs {
				cands = append(cands, taggedCandidate{Candidate: c, Source: "wap", ID: mp4VID})
			}
			known[mp4VID] = true
		}
		addImageURL(wapInfo.Image)
	}

	for _, imageURL := range imageURLs {
		a.logger.Info("downloading referenced image", zap.String("url", imageURL))
		recs, imageErr := a.download(ctx, imageURL)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if imageErr != nil {
			if errors.Is(imageErr, context.Canceled) || errors.Is(imageErr, context.DeadlineExceeded) {
				return allWarcRecEvents, imageErr
			}
			if !isHTTPStatus(imageErr, 404) {
				return allWarcRecEvents, imageErr
			}
			a.logger.Info("referenced image unavailable", zap.String("url", imageURL), zap.Error(imageErr))
		}
	}

	for _, group := range a.groupCandidatesByETag(cands) {
		primary := group[0]
		name := fmt.Sprintf("%s.%s", primary.ID, primary.Ext)
		recs, savedURL, downloadErr := a.downloadCandidateGroup(ctx, name, group)
		allWarcRecEvents = append(allWarcRecEvents, recs...)
		if downloadErr != nil {
			return allWarcRecEvents, downloadErr
		}
		if savedURL == "" {
			a.logger.Info("media unavailable after retries; preserving partial archive", zap.String("filename", name), zap.Int("mirrors", len(group)))
			continue
		}
		meta.Files = append(meta.Files, FileRef{VID: primary.ID, Ext: primary.Ext, Size: primary.Size, Filename: name, Source: primary.Source})
		a.logger.Info("saved media", zap.String("filename", name), zap.String("url", savedURL))
	}

	a.logger.Info("finished VID archive", zap.Int("files", len(meta.Files)))
	return allWarcRecEvents, nil
}

func (a *Archiver) downloadCandidateGroup(ctx context.Context, filename string, group []taggedCandidate) (events []warc.RecordEvent, savedURL string, err error) {
	var hardErrors []error
	for _, candidate := range group {
		a.logger.Info("downloading media", zap.String("filename", filename), zap.String("url", candidate.URL), zap.Int64("size", candidate.Size))
		records, downloadErr := a.download(ctx, candidate.URL)
		events = append(events, records...)
		if downloadErr == nil {
			return events, candidate.URL, nil
		}
		if ctx.Err() != nil {
			return events, "", context.Cause(ctx)
		}
		a.logger.Info("media download failed", zap.String("filename", filename), zap.String("url", candidate.URL), zap.Error(downloadErr))
		if !isHTTPStatus(downloadErr, 404) {
			hardErrors = append(hardErrors, fmt.Errorf("download %s from %s: %w", filename, candidate.URL, downloadErr))
		}
	}
	return events, "", errors.Join(hardErrors...)
}

func (a *Archiver) groupCandidatesByETag(candidates []taggedCandidate) [][]taggedCandidate {
	groupIndexes := make(map[string]int)
	groups := make([][]taggedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.ETag
		if key == "" {
			key = "\x00" + candidate.URL
		}
		if index, ok := groupIndexes[key]; ok {
			groups[index] = append(groups[index], candidate)
			a.logger.Info("dedupe fallback",
				zap.String("url", candidate.URL),
				zap.String("primary_url", groups[index][0].URL),
				zap.String("etag", candidate.ETag),
			)
			continue
		}
		groupIndexes[key] = len(groups)
		groups = append(groups, []taggedCandidate{candidate})
	}
	return groups
}
