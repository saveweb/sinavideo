package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
)

var exts = []string{"mp4", "flv", "hlv"}

var sourceServers = []string{ // To save bandwidth, cdns are disabled
	// "http://cdn.sinacloud.net/edge.v.iask.com/%s.%s",
	"http://sinacloud.net/edge.v.iask.com/%s.%s",
	// "http://cdn.sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
	"http://sinacloud.net/edge.ivideo.sina.com.cn/%s.%s",
}

func probeSRC(vid string) (url string, ext string, size int64, ok bool) {
	for _, srv := range sourceServers {
		for _, e := range exts {
			u := fmt.Sprintf(srv, vid, e)
			log.Println("probeSRC", u)
			resp, err := client.Head(u)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == 200 {
				x_sz, _ := strconv.ParseInt(resp.Header.Get("X-Filesize"), 10, 64)
				cl_sz, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
				return u, e, max(x_sz, cl_sz), true
			}
		}
	}
	return "", "", 0, false
}

func download(url, path string) error {
	tmp := path + ".tmp"
	log.Println("download", url, "->", tmp)
	r, err := client.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("http %d", r.StatusCode)
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}
