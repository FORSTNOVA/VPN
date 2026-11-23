package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Compare the real framed host executable with the production handlers on an
// idle app. No controller, store, sockets or user profile are needed.
func TestOfflineHostIdleParity(t *testing.T) {
	host := os.Getenv("SMARTVPN_TEST_OFFLINE_HOST")
	if host == "" {
		t.Skip("offline Rust host binary was not supplied")
	}
	a := &app{}
	for path, handler := range map[string]http.HandlerFunc{
		"/api/latency": a.latency, "/api/nodes/latency": a.nodeLatency, "/api/traffic": a.traffic,
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler(recorder, httptest.NewRequest("GET", path, nil))
			for _, method := range []string{"GET", "HEAD"} {
				order := binary.LittleEndian
				frame := append(order.AppendUint64(nil, 1), 1)
				for _, field := range []string{method, path, "Bearer offline-synthetic"} {
					frame = append(order.AppendUint32(frame, uint32(len(field))), field...)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, host, "--offline-stdio")
				cmd.Stdin = bytes.NewReader(append(order.AppendUint32(nil, uint32(len(frame))), frame...))
				got, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				body := recorder.Body.Bytes()
				if method == "HEAD" {
					body = nil
				}
				want := order.AppendUint16(nil, uint16(recorder.Code))
				want = order.AppendUint64(want, 0)
				want = append(order.AppendUint32(want, uint32(len(body))), body...)
				want = append(order.AppendUint32(nil, uint32(len(want))), want...)
				if !bytes.Equal(got, want) {
					t.Fatalf("%s %s: got %q want %q", method, path, got, want)
				}
			}
		})
	}
}
