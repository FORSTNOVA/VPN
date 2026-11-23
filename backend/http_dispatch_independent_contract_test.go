package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestHTTPDispatchIndependentFixedGoldens(t *testing.T) {
	for _, c := range []struct {
		method, path, auth string
		status             int
		allow              string
		calls              int
	}{{"GET", "/api/state", "", 401, "", 0}, {"POST", "/api/state", "", 405, "GET, HEAD", 0}, {"HEAD", "/api/state", "Bearer synthetic", 202, "", 1}, {"OPTIONS", "/api/state", "Bearer synthetic", 405, "GET, HEAD", 0}, {"GET", "/missing", "", 404, "", 0}, {"GET", "//api/state", "", 301, "", 0}, {"GET", "/api/identify", "", 202, "", 1}, {"GET", "/api/state/", "Bearer synthetic", 404, "", 0}, {"GET", "/api/%73tate", "Bearer synthetic", 202, "", 1}} {
		calls := 0
		m := dispatchIndependentMux(true, "synthetic", &calls)
		q := httptest.NewRequest(c.method, c.path, nil)
		q.Header.Set("Authorization", c.auth)
		w := httptest.NewRecorder()
		m.ServeHTTP(w, q)
		if w.Code != c.status || w.Header().Get("Allow") != c.allow || calls != c.calls {
			t.Fatalf("fixed %v got %d allow%q calls%d", c, w.Code, w.Header().Get("Allow"), calls)
		}
		if c.status == 401 && (w.Body.String() != "unauthorized\n" || w.Header().Get("X-Content-Type-Options") != "nosniff") {
			t.Fatal("unauthorized body/header")
		}
	}
}
func TestHTTPDispatchIndependentDecoder(t *testing.T) {
	for _, c := range []struct {
		body  string
		ok    bool
		value int
	}{{`{"x":1}`, true, 1}, {`{"x":1}{"unknown":2}`, true, 1}, {`{"x":1} garbage`, true, 1}, {`{"x":1,"unknown":2}`, false, 1}, {`{"x":1,"x":2}`, true, 2}, {`null`, true, 0}, {strings.Repeat(" ", 65536) + `{"x":1}`, false, 0}, {`{"x":`, false, 0}, {`{"x":"bad"}`, false, 0}} {
		var dst struct {
			X int `json:"x"`
		}
		e := decodeJSON(strings.NewReader(c.body), &dst)
		if (e == nil) != c.ok || dst.X != c.value {
			t.Fatalf("decode len%d ok%v value%d err%v", len(c.body), c.ok, dst.X, e)
		}
	}
	var dst map[string]any
	if e := decodeJSON(strings.NewReader(`{"x":1}`), &dst); e != nil {
		t.Fatal(e)
	}
	if _, ok := dst["x"].(float64); !ok {
		t.Fatal("number codec changed")
	}

}
func TestHTTPDispatchIndependentPanicOnce(t *testing.T) {
	calls := 0
	w := httptest.NewRecorder()
	q := httptest.NewRequest("GET", "/api/state", nil)
	q.Header.Set("Authorization", "Bearer synthetic")
	marker := &struct{}{}
	h := httpDispatchWrapper("GET /api/state", "synthetic", 2, false, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(207)
		_, _ = w.Write([]byte("committed"))
		panic(marker)
	})
	var got any
	func() { defer func() { got = recover() }(); h(w, q) }()
	if got != marker || calls != 1 || w.Code != 207 || w.Body.String() != "committed" {
		t.Fatalf("panic/replay calls%d status%d body%q", calls, w.Code, w.Body.String())
	}
}

func TestHTTPDispatchIndependentProductionRegistration(t *testing.T) {
	want := len(dispatchIndependentPatterns)
	if runtime.GOOS != "windows" {
		want--
	}
	routes := (&app{}).apiRoutes()
	seen := map[uint32]bool{}
	if len(routes) != want {
		t.Fatalf("registered %d want %d", len(routes), want)
	}
	for _, r := range routes {
		pattern := r.method + " " + r.path
		if r.id == 0 || int(r.id) > want || dispatchIndependentPatterns[r.id-1] != pattern || seen[r.id] || r.handle == nil {
			t.Fatalf("unexpected registration %d %s", r.id, pattern)
		}
		seen[r.id] = true
	}
}

func TestHTTPDispatchIndependentAuthorizationHeaderOrder(t *testing.T) {
	for _, c := range []struct {
		values []string
		want   int
	}{{[]string{"bad", "Bearer synthetic"}, 401}, {[]string{"Bearer synthetic", "bad"}, 202}, {[]string{"Bearer synthetic\t"}, 401}, {[]string{"Bearer\tsynthetic"}, 401}} {
		calls := 0
		m := dispatchIndependentMux(true, "synthetic", &calls)
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.Header["Authorization"] = c.values
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatalf("header order %q=>%d", c.values, w.Code)
		}
	}
}
