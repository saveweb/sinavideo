package vl

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type VictoriaLogsAsyncWriter struct {
	url         string
	client      *http.Client
	logChan     chan []byte
	done        chan struct{}
	mu          sync.Mutex
	closed      bool
	maxBatch    int
	flushPeriod time.Duration
}

func NewVLWriter(vlAddr, query string, maxQueueSize, maxBatch int, flushPeriod time.Duration) *VictoriaLogsAsyncWriter {
	w := &VictoriaLogsAsyncWriter{
		url:         fmt.Sprintf("%s/insert/jsonline?%s", vlAddr, query),
		client:      &http.Client{Timeout: 10 * time.Second},
		logChan:     make(chan []byte, maxQueueSize),
		done:        make(chan struct{}),
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

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(p), nil
	}
	select {
	case w.logChan <- buf:
		LogsEnqueued.Add(1)
	default:
		LogsDropped.Add(1)
		fmt.Fprintf(os.Stderr, "[VL-Writer-Error] log queue full; enqueued=%d dropped=%d sent=%d failed=%d\n", LogsEnqueued.Load(), LogsDropped.Load(), LogsSent.Load(), LogsFailed.Load())
	}
	return len(p), nil
}

func (w *VictoriaLogsAsyncWriter) worker() {
	defer close(w.done)
	ticker := time.NewTicker(w.flushPeriod)
	defer ticker.Stop()

	var batch bytes.Buffer
	count := 0

	send := func() {
		if batch.Len() == 0 {
			return
		}
		req, err := http.NewRequest("POST", w.url, bytes.NewReader(batch.Bytes()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[VL-Writer-Error] failed to create request: %v\n", err)
			LogsFailed.Add(int64(count))
			batch.Reset()
			count = 0
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := w.client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[VL-Writer-Error] failed to send logs: %v\n", err)
			LogsFailed.Add(int64(count))
			batch.Reset()
			count = 0
			return
		}
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "[VL-Writer-Error] VictoriaLogs returned status: %d\n", resp.StatusCode)
			LogsFailed.Add(int64(count))
		} else {
			LogsSent.Add(int64(count))
		}

		batch.Reset()
		count = 0
	}

	for {
		select {
		case logLine, ok := <-w.logChan:
			if !ok {
				send()
				return
			}
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
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.logChan)
	}
	w.mu.Unlock()
	<-w.done
}
