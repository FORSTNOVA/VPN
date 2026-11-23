package main

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"
)

type monitorTarget struct {
	addr, token string
	lifetime    context.Context
}
type monitorProvider interface{ monitorEndpoint() (monitorTarget, bool) }

var localMonitorClient = &http.Client{
	Transport:     &http.Transport{DisableKeepAlives: true},
	Timeout:       45 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Called behind the existing service token check. Capture the current runner
// under the app lock, then release it before doing any network I/O.
func (a *app) monitored(path string, local http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		var target monitorTarget
		var ok bool
		nodeTimeout := 0
		if provider, found := a.kernel.(monitorProvider); found {
			target, ok = provider.monitorEndpoint()
			if ok && path == "/api/nodes/latency" {
				nodeTimeout = a.healthMetaLocked().Params.ConnectTimeoutMs
			}
		}
		a.mu.Unlock()
		if !ok {
			local(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if target.lifetime.Err() != nil {
			http.Error(w, "Rust monitor unavailable", 502)
			return
		}
		stop := context.AfterFunc(target.lifetime, cancel)
		defer stop()
		req, err := http.NewRequestWithContext(ctx, r.Method, "http://"+target.addr+path, nil)
		if err != nil {
			http.Error(w, "invalid monitor endpoint", 502)
			return
		}
		req.Header.Set("Authorization", "Bearer "+target.token)
		if nodeTimeout != 0 {
			req.Header.Set("X-SmartVPN-Node-Timeout-Ms", strconv.Itoa(nodeTimeout))
		}
		res, err := localMonitorClient.Do(req)
		if err != nil {
			http.Error(w, "Rust monitor unavailable", 502)
			return
		}
		defer res.Body.Close()
		for _, key := range []string{"Content-Type", "Content-Length"} {
			if value := res.Header.Get(key); value != "" {
				w.Header().Set(key, value)
			}
		}
		w.WriteHeader(res.StatusCode)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, res.Body)
		}
	}
}
