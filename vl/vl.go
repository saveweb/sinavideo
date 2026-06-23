package vl

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

type VictoriaLogsAsyncWriter struct {
	url         string
	client      *http.Client
	logChan     chan []byte
	ctx         context.Context
	cancel      context.CancelFunc
	maxBatch    int
	flushPeriod time.Duration
}

func NewVLWriter(vlAddr, query string, maxQueueSize, maxBatch int, flushPeriod time.Duration) *VictoriaLogsAsyncWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &VictoriaLogsAsyncWriter{
		url:         fmt.Sprintf("%s/insert/jsonline?%s", vlAddr, query),
		client:      &http.Client{Timeout: 10 * time.Second},
		logChan:     make(chan []byte, maxQueueSize),
		ctx:         ctx,
		cancel:      cancel,
		maxBatch:    maxBatch,
		flushPeriod: flushPeriod,
	}

	go w.worker()
	return w
}

var LogsEnqueued atomic.Int64
var LogsDropped atomic.Int64
var LogsSent atomic.Int64
var LogsFailed atomic.Int64

func (w *VictoriaLogsAsyncWriter) Write(p []byte) (n int, err error) {
	buf := make([]byte, len(p))
	copy(buf, p)

	select {
	case w.logChan <- buf:
		LogsEnqueued.Add(1)
	default:
		LogsDropped.Add(1)
		log.Println("[VL-Writer-Error] Log queue full, dropping log line. logs metrics:", "enqueued", LogsEnqueued.Load(), "dropped", LogsDropped.Load(), "sent", LogsSent.Load(), "failed", LogsFailed.Load())
	}
	return len(p), nil
}

func (w *VictoriaLogsAsyncWriter) worker() {
	ticker := time.NewTicker(w.flushPeriod)
	defer ticker.Stop()

	var batch bytes.Buffer
	count := 0

	send := func() {
		if batch.Len() == 0 {
			return
		}
		req, err := http.NewRequestWithContext(w.ctx, "POST", w.url, bytes.NewReader(batch.Bytes()))
		if err != nil {
			log.Printf("[VL-Writer-Error] failed to create request: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := w.client.Do(req)
		if err != nil {
			log.Printf("[VL-Writer-Error] failed to send logs: %v\n", err)
			LogsFailed.Add(int64(count))
			return
		}
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			log.Printf("[VL-Writer-Error] VictoriaLogs returned status: %d\n", resp.StatusCode)
			LogsFailed.Add(int64(count))
		}

		if resp.StatusCode < 200 {
			LogsSent.Add(int64(count))
		}

		batch.Reset()
		count = 0
	}

	for {
		select {
		case <-w.ctx.Done():
			send()
			return
		case logLine := <-w.logChan:
			batch.Write(logLine)
			count++
			if count >= w.maxBatch {
				send()
			}
		case <-ticker.C:
			send()
		}
	}
}

func (w *VictoriaLogsAsyncWriter) Close() {
	w.cancel()
	time.Sleep(500 * time.Millisecond)
}
