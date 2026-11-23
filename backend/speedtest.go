package main

import (
	"errors"
	"io"
	"net/http"
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
func throughputMbpsGo(bytes int64, elapsed time.Duration) float64 {
	if bytes <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(bytes) * 8 / 1e6 / elapsed.Seconds()
}

// measureDownload pulls from several streams at once. They share one byte
// allowance and one deadline, and the clock starts when the first byte lands, so
// the connection setup is not charged to the rate.
func measureDownload(proxyAddress string, allowance int64, budget time.Duration) (int64, time.Duration, error) {
	q := speedServiceRequest{Op: "download", Allowance: allowance, Budget: budget}
	e := newAppSpeedEffects(nil, q, nil)
	e.proxy = proxyAddress
	r := runSpeedServiceWithEffects(q, e)
	return r.Bytes, r.Elapsed, r.Err
}

// measureUpload streams zeros at the endpoint and counts what the transport
// takes. The socket's write side applies the back pressure, so what the reader
// hands over is what the link is carrying.
func measureUpload(proxyAddress string, allowance int64, budget time.Duration) (int64, time.Duration, error) {
	q := speedServiceRequest{Op: "upload", Allowance: allowance, Budget: budget}
	e := newAppSpeedEffects(nil, q, nil)
	e.proxy = proxyAddress
	r := runSpeedServiceWithEffects(q, e)
	return r.Bytes, r.Elapsed, r.Err
}

// zeroReader is an endless source of zeros that reports how much has been taken.
type zeroReader struct {
	remaining int64
	started   *atomic.Int64
	total     *atomic.Int64
	runtime   *speedRuntime
	batch     *speedBatch
}

func (r *zeroReader) Read(target []byte) (int, error) {
	rt := r.runtime
	if rt == nil {
		rt = defaultSpeedRuntime()
	}
	q := speedServiceRequest{Op: "zero-read", Remaining: r.remaining, Length: len(target)}
	e := newSpeedEffects(nil, q, nil, rt)
	e.zero = r
	e.target = target
	v := runSpeedServiceWithEffects(q, e)
	if v.Err != nil {
		if r.batch != nil {
			r.batch.mu.Lock()
			if r.batch.failure == nil {
				r.batch.failure = v.Err
			}
			r.batch.mu.Unlock()
		}
		return 0, v.Err
	}
	if v.EOF {
		return 0, io.EOF
	}
	return int(v.Bytes), nil
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
	q := speedServiceRequest{Op: "handler", MegaBytes: megaBytes}
	e := newAppSpeedEffects(a, q, w)
	_ = runSpeedServiceWithEffects(q, e)
}
