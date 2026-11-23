package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// This freezes a legacy quirk: an empty read starts the clock even though no
// bytes have moved. It does not contact an endpoint or create any timer.
func TestSpeedServiceIndependentZeroReaderLegacy(t *testing.T) {
	var started, total atomic.Int64
	r := &zeroReader{remaining: 3, started: &started, total: &total}
	if n, e := r.Read(nil); n != 0 || e != nil || started.Load() == 0 || total.Load() != 0 || r.remaining != 3 {
		t.Fatalf("empty read changed legacy contract: n=%d err=%v started=%d total=%d remaining=%d", n, e, started.Load(), total.Load(), r.remaining)
	}
	first := started.Load()
	b := []byte{9, 8, 7, 6, 5}
	if n, e := r.Read(b); n != 3 || e != nil || total.Load() != 3 || started.Load() != first {
		t.Fatalf("final payload read: %d %v", n, e)
	}
	if b[0] != 0 || b[1] != 0 || b[2] != 0 || b[3] != 6 || b[4] != 5 {
		t.Fatalf("wrote outside returned payload: %v", b)
	}
	if n, e := r.Read(nil); n != 0 || e != io.EOF {
		t.Fatalf("exhaustion has priority over empty buffer: %d %v", n, e)
	}
}

func TestSpeedServiceIndependentHTTPInvalidJSON(t *testing.T) {
	for _, body := range []string{"{", `{"megaBytes":"bad"}`, `{"megaBytes":1.5}`, `{"megaBytes":999999999999999999999999}`} {
		t.Run(body, func(t *testing.T) {
			// A zero app is deliberate: rejection must precede all app/kernel access.
			a := &app{}
			w := httptest.NewRecorder()
			a.speedTest(w, httptest.NewRequest("POST", "/api/speedtest", strings.NewReader(body)))
			if w.Code != 400 || w.Body.String() != "invalid request body\n" {
				t.Fatalf("response %d %q", w.Code, w.Body.String())
			}
		})
	}
}
