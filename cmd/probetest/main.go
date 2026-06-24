// probetest: 不依赖 WARC/tracker/tus 的探测逻辑 dry-run 验证工具。
// 它镜像 cmd/archive/cdn.go 的 probeCandidates + dedupeByETag 逻辑，
// 对一批 vid 执行「全源全 ext 探测 → ETag 去重」，打印最终会下载哪些 URL，
// 用以验证覆盖率与去重是否如预期，而不真正下载 body。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var sourceServers = []string{
	"http://s3.ivideo.sina.com.cn/%s.%s",
	"http://sinacloud.net/edge.v.iask.com/%s.%s",
	"http://sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
}

var exts = []string{"mp4", "flv", "hlv"}

type Candidate struct {
	URL  string
	Ext  string
	Size int64
	ETag string
}

func probeCandidates(id string, extList []string, c *http.Client) []Candidate {
	var cands []Candidate
	for _, srv := range sourceServers {
		for _, e := range extList {
			u := fmt.Sprintf(srv, id, e)
			req, _ := http.NewRequest("HEAD", u, nil)
			r, err := c.Do(req)
			if err != nil {
				continue
			}
			r.Body.Close()
			if r.StatusCode == 200 {
				xsz, _ := strconv.ParseInt(r.Header.Get("X-Filesize"), 10, 64)
				clsz, _ := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
				sz := xsz
				if clsz > sz {
					sz = clsz
				}
				cands = append(cands, Candidate{URL: u, Ext: e, Size: sz, ETag: strings.TrimSpace(r.Header.Get("ETag"))})
			}
		}
	}
	return cands
}

func dedupeByETag(cands []Candidate) []Candidate {
	seen := map[string]bool{}
	var out []Candidate
	for _, c := range cands {
		key := c.ETag
		if key == "" {
			key = "\x00" + c.URL
		}
		if seen[key] {
			log.Printf("    dedupe skip %s (etag=%s)", c.URL, c.ETag)
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// 模拟 archive.go 的 id 收集逻辑（精简版，不依赖 WARC client）。
func curl(url string, c *http.Client) string {
	req, _ := http.NewRequest("GET", url, nil)
	r, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func archiveDryRun(vid string, c *http.Client) {
	fmt.Printf("\n========== VID %s ==========\n", vid)

	// getvideoidbyvid (严格 JSON 解析，与 api.go 一致)
	body := curl("https://s.video.sina.com.cn/video/getvideoidbyvid?vid="+vid, c)
	videoID := ""
	var vidResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(body), &vidResp) == nil && vidResp.Code == 1 {
		if s, ok := vidResp.Data["video_id"].(string); ok {
			videoID = s
		}
	}
	if videoID == "" {
		fmt.Printf("  [API] getvideoidbyvid 失败: %s\n", truncate(body, 80))
	}
	fmt.Printf("  video_id = %s\n", videoID)

	// play API (严格 JSON 解析，与 api.go 一致)
	fileids := []string{vid}
	if videoID != "" {
		pbody := curl("http://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id="+videoID, c)
		var play struct {
			Code int `json:"code"`
			Data struct {
				Videos []struct {
					FileID string `json:"file_id"`
				} `json:"videos"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(pbody), &play) == nil && play.Code == 1 {
			for _, v := range play.Data.Videos {
				if v.FileID != "" {
					fileids = append(fileids, v.FileID)
				}
			}
		} else {
			fmt.Printf("  [API] play 失败: %s\n", truncate(pbody, 80))
		}
		// 去重
		seen := map[string]bool{}
		uniq := fileids[:0]
		for _, f := range fileids {
			if !seen[f] {
				seen[f] = true
				uniq = append(uniq, f)
			}
		}
		fileids = uniq
	}
	fmt.Printf("  主档 ids (vid + fileids) = %v\n", fileids)

	// 探测主档候选
	var allCands []Candidate
	for _, id := range fileids {
		cs := probeCandidates(id, exts, c)
		allCands = append(allCands, cs...)
	}
	fmt.Printf("  主档探测命中: %d 个候选\n", len(allCands))

	// ipad_vid (严格 JSON 解析，处理 string|false 混合类型，与 api.go getIpadVID 一致)
	ibody := curl("http://video.sina.com.cn/interface/video_ids/video_ids.php?v="+vid, c)
	ipadVID := ""
	var ipadResp struct {
		IpadVID json.RawMessage `json:"ipad_vid"`
	}
	if json.Unmarshal([]byte(ibody), &ipadResp) == nil {
		raw := strings.TrimSpace(string(ipadResp.IpadVID))
		if raw == "false" || raw == "" {
			fmt.Printf("  ipad_vid = false (无低清版，跳过)\n")
		} else {
			var s string
			if json.Unmarshal(ipadResp.IpadVID, &s) == nil {
				ipadVID = s
			}
		}
	}
	if ipadVID != "" {
		fmt.Printf("  ipad_vid = %s\n", ipadVID)
		if ipadVID != vid && !contains(fileids, ipadVID) {
			cs := probeCandidates(ipadVID, []string{"mp4"}, c)
			fmt.Printf("  ipad 探测命中: %d 个候选\n", len(cs))
			allCands = append(allCands, cs...)
		} else {
			fmt.Printf("  ipad_vid %s 已在主档 ids 中，跳过(避免重复)\n", ipadVID)
		}
	}

	// ETag 去重
	fmt.Printf("  去重前候选总数: %d\n", len(allCands))
	uniq := dedupeByETag(allCands)
	fmt.Printf("  去重后候选总数: %d (节省 %d 次重复下载)\n", len(uniq), len(allCands)-len(uniq))

	// 最终将下载的 URL
	fmt.Printf("  --- 将下载 ---\n")
	var totalBytes int64
	for _, u := range uniq {
		fmt.Printf("    [%s] %s  (%d bytes, etag=%s)\n", u.Ext, u.URL, u.Size, u.ETag)
		totalBytes += u.Size
	}
	fmt.Printf("  合计 %d 个文件, %.2f MB\n", len(uniq), float64(totalBytes)/1024/1024)
}

func main() {
	c := &http.Client{Timeout: 15 * time.Second}
	vids := os.Args[1:]
	if len(vids) == 0 {
		fmt.Fprintln(os.Stderr, "usage: probetest <vid> [vid ...]")
		os.Exit(1)
	}
	for _, v := range vids {
		archiveDryRun(v, c)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
