package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type httpDispatchTestEffects struct {
	events      []string
	panicInvoke bool
}

func (e *httpDispatchTestEffects) Unauthorized() { e.events = append(e.events, "401") }
func (e *httpDispatchTestEffects) JSONHeader()   { e.events = append(e.events, "json") }
func (e *httpDispatchTestEffects) Invoke(_ uint32) {
	e.events = append(e.events, "invoke")
	if e.panicInvoke {
		panic("committed")
	}
}
func TestHTTPDispatchServiceExactlyOnce(t *testing.T) {
	e := &httpDispatchTestEffects{panicInvoke: true}
	func() {
		defer func() {
			if recover() != "committed" {
				t.Fatal("panic swallowed")
			}
		}()
		executeHTTPDispatch(httpDispatchPlan{Route: 2, JSON: true}, e)
	}()
	if len(e.events) != 2 || e.events[0] != "json" || e.events[1] != "invoke" {
		t.Fatal(e.events)
	}
}
func TestHTTPDispatchServiceWriterIdentity(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Header.Set("Authorization", "Bearer t")
	n := 0
	httpDispatchWrapper("GET /api/state", "t", 2, false, func(actual http.ResponseWriter, req *http.Request) {
		n++
		if actual != w || req != r {
			t.Fatal("resource replaced")
		}
		actual.WriteHeader(202)
		_, _ = actual.Write([]byte("body"))
	})(w, r)
	if n != 1 || w.Code != 202 || w.Body.String() != "body" {
		t.Fatal(n, w)
	}
}
