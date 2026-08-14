package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
)

var exts = []string{"mp4", "flv", "hlv"}

const downloadTimeout = 30 * time.Minute

const mediaDownloadAttempts = 3

var mediaDownloadBackoff = [...]time.Duration{time.Second, 3 * time.Second}

type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http %d", e.code)
}

func (e *httpStatusError) StatusCode() int {
	return e.code
}

// sourceServers 按探测/下载优先级排列。三个入口共享部分数据（同 vid 三源都命中时 ETag 一致），
// 但各自都有独占文件——批量测试（72 样本 × 3 源 × 3 ext）显示 s3.ivideo / edge.ivideo /
// edge.v.iask 都存在"只有自己命中、另两个 404"的文件。因此必须探测全部源以保证召回率。
// 排在最前的源在 ETag 去重后会被优先选为下载源。
// CDN 入口（cdn.sinacloud.net）已注释关闭以节省带宽；必要时可取消注释。
var sourceServers = []string{
	// sina_api.md 推荐的源站，当前最大的活桶（5.27 亿对象）
	"http://s3.ivideo.sina.com.cn/%s.%s", // http://sinacloud.net/s3.ivideo.sina.com.cn/
	// 直连存储桶
	"http://sinacloud.net/edge.v.iask.com/%s.%s",
	"http://sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
	// "http://cdn.sinacloud.net/edge.v.iask.com/%s.%s",
	// "http://cdn.sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
}

// Candidate 是一次探测命中的候选下载项。
type Candidate struct {
	URL  string
	Ext  string
	Size int64
	ETag string
}

// probeCandidates 对 id 在「所有源 × 给定扩展名」上发 HEAD，返回所有 200 命中的候选。
// 故意探测全部源而非命中即停：三个源覆盖范围是部分重叠的并集，某个源 404 的文件可能在另一源上存在。
func (a *Archiver) probeCandidates(ctx context.Context, id string, extList []string) (cands []Candidate, allRecEvents []warc.RecordEvent, err error) {
	for _, srv := range sourceServers {
		for _, e := range extList {
			if err := ctx.Err(); err != nil {
				return cands, allRecEvents, context.Cause(ctx)
			}
			u := fmt.Sprintf(srv, id, e)
			log.Println("probe", u)
			req, err := http.NewRequestWithContext(ctx, "HEAD", u, nil)
			if err != nil {
				continue
			}
			r, events, err := a.executeWARCRequest(req, func(*http.Response) error { return nil })
			allRecEvents = append(allRecEvents, events...)
			if err != nil {
				if ctx.Err() != nil {
					return cands, allRecEvents, err
				}
				continue
			}
			if r.StatusCode == 200 {
				x_sz, _ := strconv.ParseInt(r.Header.Get("X-Filesize"), 10, 64)
				cl_sz, _ := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
				cands = append(cands, Candidate{
					URL:  u,
					Ext:  e,
					Size: max(x_sz, cl_sz),
					ETag: strings.TrimSpace(r.Header.Get("ETag")),
				})
			}
		}
	}
	return cands, allRecEvents, nil
}

// dedupeByETag 把指向同一内容（ETag 相同）的候选合并为一次下载。
// 三个源共享大量数据，同一文件通常能通过 3 个 URL 访问且 ETag 一致；按 ETag 去重即可
// 在最大化覆盖率的同时避免重复字节/请求。没有 ETag 的候选无法匹配，各自保留（宁可多下也不漏）。
func dedupeByETag(cands []Candidate) []Candidate {
	seen := map[string]bool{}
	var out []Candidate
	for _, c := range cands {
		key := c.ETag
		if key == "" {
			// 无 ETag → 视为每个 URL 独立内容，保留
			key = "\x00" + c.URL
		}
		if seen[key] {
			log.Printf("  dedupe skip %s (etag=%s)", c.URL, c.ETag)
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func (a *Archiver) download(ctx context.Context, url string) ([]warc.RecordEvent, error) {
	return a.downloadWithRetry(ctx, url, downloadTimeout, mediaDownloadAttempts, func(attempt int) time.Duration {
		return mediaDownloadBackoff[attempt-1]
	})
}

func (a *Archiver) downloadWithRetry(ctx context.Context, url string, timeout time.Duration, attempts int, backoff func(int) time.Duration) ([]warc.RecordEvent, error) {
	var allRecordEvents []warc.RecordEvent
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		recordEvents, err := a.downloadWithTimeout(ctx, url, timeout)
		allRecordEvents = append(allRecordEvents, recordEvents...)
		if err == nil {
			return allRecordEvents, nil
		}
		lastErr = err
		var statusErr *httpStatusError
		if ctx.Err() != nil || errors.As(err, &statusErr) || attempt == attempts {
			break
		}

		delay := backoff(attempt)
		a.logger.Warn("download failed, retrying", zap.String("url", url), zap.Int("attempt", attempt), zap.Int("max_attempts", attempts), zap.Duration("backoff", delay), zap.Error(err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return allRecordEvents, context.Cause(ctx)
		case <-timer.C:
		}
	}
	return allRecordEvents, fmt.Errorf("download %s failed after %d attempt(s): %w", url, attempts, lastErr)
}

func (a *Archiver) downloadWithTimeout(ctx context.Context, url string, timeout time.Duration) ([]warc.RecordEvent, error) {
	a.logger.Info("download", zap.String("url", url), zap.Duration("timeout", timeout))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	_, recordsEvents, err := a.executeWARCRequest(req, func(r *http.Response) error {
		if r.StatusCode != http.StatusOK {
			return &httpStatusError{code: r.StatusCode}
		}
		// Reading to EOF lets the transport establish the response boundary and
		// retain the connection for keepalive reuse.
		n, err := io.Copy(io.Discard, r.Body)
		if err == nil {
			a.logger.Info("downloaded", zap.String("url", url), zap.Int64("size", n))
		}
		return err
	})
	return recordsEvents, err
}
