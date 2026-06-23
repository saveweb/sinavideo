package main

import (
	"fmt"
	"io"
	"log"
	"strconv"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
	"go.uber.org/zap"
)

var exts = []string{"mp4", "flv", "hlv"}

var sourceServers = []string{ // To save bandwidth, cdns are disabled
	// "http://cdn.sinacloud.net/edge.v.iask.com/%s.%s",
	"http://sinacloud.net/edge.v.iask.com/%s.%s",
	// "http://cdn.sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
	"http://sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
}

func probeSRC(vid string) (url string, ext string, size int64, allRecEvents []warc.RecordEvent, ok bool) {
	for _, srv := range sourceServers {
		for _, e := range exts {
			u := fmt.Sprintf(srv, vid, e)
			log.Println("probeSRC", u)
			req, err := http.NewRequest("HEAD", u, nil)
			if err != nil {
				continue
			}
			feedbackCh := make(chan warc.FeedbackEvent, 1)
			reqCtx := req.Context()
			reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

			r, err := client.Do(req.WithContext(reqCtx))
			if err != nil {
				continue
			}
			r.Body.Close()
			allRecEvents = append(allRecEvents, <-feedbackCh...)
			if r.StatusCode == 200 {
				x_sz, _ := strconv.ParseInt(r.Header.Get("X-Filesize"), 10, 64)
				cl_sz, _ := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
				return u, e, max(x_sz, cl_sz), allRecEvents, true
			}
		}
	}
	return "", "", 0, nil, false
}

func download(url string) (recordsEvents []warc.RecordEvent, err error) {
	// tmp := path + ".tmp"
	log.Println("download", url)
	req, _ := http.NewRequest("GET", url, nil)

	feedbackCh := make(chan warc.FeedbackEvent, 1)
	defer func() {
		recordsEvents = <-feedbackCh
	}()
	reqCtx := req.Context()
	reqCtx = warc.WithFeedbackChannel(reqCtx, feedbackCh)

	r, err := client.Do(req.WithContext(reqCtx))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return recordsEvents, fmt.Errorf("http %d", r.StatusCode)
	}
	// f, err := os.Create(tmp)
	// if err != nil {
	// 	return err
	// }
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		return recordsEvents, err
	}
	logger.Info("download", zap.String("url", url), zap.Int64("size", n))
	return recordsEvents, nil
}
