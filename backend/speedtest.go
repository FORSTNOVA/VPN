package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The speed test measures throughput through the node — the same data path the
// browser takes — rather than a theoretical line rate. Cloudflare's endpoint is
// used because it needs no key, it is neutral, and it is fast from most places
// these nodes sit in.
const (
	speedTestHost      = "speed.cloudflare.com"
	speedTestOrigin    = "https://" + speedTestHost
	speedTestSeconds   = 10 * time.Second
	speedTestStreams   = 4
	speedUploadStreams = 2
	// The upload budget is a third of the download's: it is the smaller number
	// that matters to most people, and it keeps one test from spending the whole
	// allowance.
	speedUploadShare = 3
)

type speedTestResult struct {
	Target    string  `json:"target"`
	MegaBytes int     `json:"megaBytes"`
	DownMbps  float64 `json:"downMbps"`
	UpMbps    float64 `json:"upMbps"`
	DownBytes int64   `json:"downBytes"`
	UpBytes   int64   `json:"upBytes"`
	DownMs    int64   `json:"downMs"`
	UpMs      int64   `json:"upMs"`
	Error     string  `json:"error,omitempty"`
}

// throughputMbps turns what actually moved into megabits per second. Nothing
// moved, or no time passed, means nothing to report.
func throughputMbps(bytes int64, elapsed time.Duration) float64 {
	if bytes <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(bytes) * 8 / 1e6 / elapsed.Seconds()
}

// measureDownload pulls from several streams at once. They share one byte
// allowance and one deadline, and the clock starts when the first byte lands, so
// the connection setup is not charged to the rate.
func measureDownload(proxyAddress string, allowance int64, budget time.Duration) (int64, time.Duration, error) {
	client := diagnosableClient(proxyAddress, budget+5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	var total, started atomic.Int64
	var pending sync.WaitGroup
	for i := 0; i < speedTestStreams; i++ {
		pending.Add(1)
		go func() {
			defer pending.Done()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet,
				fmt.Sprintf("%s/__down?bytes=%d", speedTestOrigin, allowance), nil)
			if err != nil {
				return
			}
			response, err := client.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			buffer := make([]byte, 64*1024)
			for {
				read, err := response.Body.Read(buffer)
				if read > 0 {
					if total.Load() == 0 {
						started.CompareAndSwap(0, time.Now().UnixNano())
					}
					total.Add(int64(read))
				}
				if err != nil || total.Load() >= allowance {
					return
				}
			}
		}()
	}
	pending.Wait()

	begin := started.Load()
	if begin == 0 || total.Load() == 0 {
		return 0, 0, errors.New("测速没有收到任何数据（节点或链路不可用）")
	}
	return total.Load(), time.Since(time.Unix(0, begin)), nil
}

// measureUpload streams zeros at the endpoint and counts what the transport
// takes. The socket's write side applies the back pressure, so what the reader
// hands over is what the link is carrying.
func measureUpload(proxyAddress string, allowance int64, budget time.Duration) (int64, time.Duration, error) {
	client := diagnosableClient(proxyAddress, budget+5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	chunk := allowance / int64(speedUploadStreams)
	var total, started atomic.Int64
	var pending sync.WaitGroup
	for i := 0; i < speedUploadStreams; i++ {
		pending.Add(1)
		go func() {
			defer pending.Done()
			source := &zeroReader{remaining: chunk, started: &started, total: &total}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, speedTestOrigin+"/__up", source)
			if err != nil {
				return
			}
			request.ContentLength = chunk
			request.Header.Set("Content-Type", "application/octet-stream")
			response, err := client.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		}()
	}
	pending.Wait()

	begin := started.Load()
	if begin == 0 || total.Load() == 0 {
		return 0, 0, errors.New("测速没有发出任何数据（节点或链路不可用）")
	}
	return total.Load(), time.Since(time.Unix(0, begin)), nil
}

// zeroReader is an endless source of zeros that reports how much has been taken.
type zeroReader struct {
	remaining int64
	started   *atomic.Int64
	total     *atomic.Int64
}

func (r *zeroReader) Read(target []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	size := int64(len(target))
	if size > r.remaining {
		size = r.remaining
	}
	for i := int64(0); i < size; i++ {
		target[i] = 0
	}
	r.remaining -= size
	r.started.CompareAndSwap(0, time.Now().UnixNano())
	r.total.Add(size)
	return int(size), nil
}

// speedTest measures the node in use and reports what moved. It is refused while
// a connection is being set up, but it starts its own kernel when nothing is
// running, exactly as the other checks do.
func (a *app) speedTest(w http.ResponseWriter, r *http.Request) {
	megaBytes := 30
	if r.Body != nil {
		var body struct {
			MegaBytes int `json:"megaBytes"`
		}
		if err := decodeJSON(r.Body, &body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if body.MegaBytes != 0 {
			megaBytes = body.MegaBytes
		}
	}
	if megaBytes < 10 {
		megaBytes = 10
	}
	if megaBytes > 100 {
		megaBytes = 100
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	temporary, err := a.ensureKernelForCheckLocked()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if temporary {
		defer a.stopMihomoLocked()
	}
	selected := a.activeNodeLocked()
	if selected == "" || strings.EqualFold(selected, blockedChoice) {
		http.Error(w, "select a subscription node before measuring speed", http.StatusConflict)
		return
	}

	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))
	result := speedTestResult{Target: speedTestHost, MegaBytes: megaBytes}

	downBytes, downElapsed, err := measureDownload(proxyAddress, int64(megaBytes)<<20, speedTestSeconds)
	if err != nil {
		result.Error = err.Error()
	} else {
		result.DownBytes = downBytes
		result.DownMs = downElapsed.Milliseconds()
		result.DownMbps = throughputMbps(downBytes, downElapsed)
	}

	upAllowance := int64(megaBytes/speedUploadShare) << 20
	upBytes, upElapsed, err := measureUpload(proxyAddress, upAllowance, speedTestSeconds)
	if err != nil {
		if result.Error == "" {
			result.Error = err.Error()
		}
	} else {
		result.UpBytes = upBytes
		result.UpMs = upElapsed.Milliseconds()
		result.UpMbps = throughputMbps(upBytes, upElapsed)
	}

	writeJSON(w, map[string]any{"node": selected, "temporary": temporary, "speed": result})
}
