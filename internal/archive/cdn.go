package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
)

var exts = []string{"mp4", "flv", "hlv"}

const downloadTimeout = 60 * time.Minute

const mediaDownloadAttempts = 3
const probeAttempts = 3

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

func statusCode(err error) (int, bool) {
	var statusErr interface{ StatusCode() int }
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	return statusErr.StatusCode(), true
}

func isHTTPStatus(err error, codes ...int) bool {
	code, ok := statusCode(err)
	if !ok {
		return false
	}
	for _, candidate := range codes {
		if code == candidate {
			return true
		}
	}
	return false
}

func shouldRetryDownload(err error) bool {
	code, ok := statusCode(err)
	if !ok {
		return true
	}
	return code == http.StatusNotFound || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func shouldRetryProbe(err error) bool {
	code, ok := statusCode(err)
	if !ok {
		return true
	}
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
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
			a.logger.Info("probe", zap.String("url", u))
			for attempt := 1; attempt <= probeAttempts; attempt++ {
				req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
				if err != nil {
					return cands, allRecEvents, err
				}
				r, events, requestErr := a.executeWARCRequest(req, func(*http.Response) error { return nil })
				allRecEvents = append(allRecEvents, events...)
				if requestErr == nil {
					switch r.StatusCode {
					case http.StatusOK:
						xSize, _ := strconv.ParseInt(r.Header.Get("X-Filesize"), 10, 64)
						contentLength, _ := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
						cands = append(cands, Candidate{URL: u, Ext: e, Size: max(xSize, contentLength), ETag: strings.TrimSpace(r.Header.Get("ETag"))})
						requestErr = nil
					case http.StatusNotFound:
						requestErr = nil
					default:
						requestErr = &httpStatusError{code: r.StatusCode}
					}
				}
				if requestErr == nil {
					break
				}
				if ctx.Err() != nil {
					return cands, allRecEvents, context.Cause(ctx)
				}
				if attempt == probeAttempts || !shouldRetryProbe(requestErr) {
					return cands, allRecEvents, fmt.Errorf("probe %s failed after %d attempt(s): %w", u, attempt, requestErr)
				}
				delay := mediaDownloadBackoff[attempt-1]
				a.logger.Warn("probe failed, retrying", zap.String("url", u), zap.Int("attempt", attempt), zap.Int("max_attempts", probeAttempts), zap.Duration("backoff", delay), zap.Error(requestErr))
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return cands, allRecEvents, context.Cause(ctx)
				case <-timer.C:
				}
			}
		}
	}
	return cands, allRecEvents, nil
}

func (a *Archiver) download(ctx context.Context, url string) ([]warc.RecordEvent, error) {
	return a.downloadWithRetry(ctx, url, downloadTimeout, mediaDownloadAttempts, func(attempt int) time.Duration {
		return mediaDownloadBackoff[attempt-1]
	})
}

func (a *Archiver) downloadWithRetry(ctx context.Context, url string, timeout time.Duration, attempts int, backoff func(int) time.Duration) ([]warc.RecordEvent, error) {
	var allRecordEvents []warc.RecordEvent
	var lastErr error
	attemptsMade := 0
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptsMade = attempt
		recordEvents, err := a.downloadWithTimeout(ctx, url, timeout)
		allRecordEvents = append(allRecordEvents, recordEvents...)
		if err == nil {
			return allRecordEvents, nil
		}
		lastErr = err
		if ctx.Err() != nil || !shouldRetryDownload(err) || attempt == attempts {
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
	return allRecordEvents, fmt.Errorf("download %s failed after %d attempt(s): %w", url, attemptsMade, lastErr)
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
