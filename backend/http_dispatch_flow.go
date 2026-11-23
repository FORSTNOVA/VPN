package main

import (
	"net/http"
	"strings"
)

// The mux remains responsible for canonicalization and method errors. This
// composition owns authorization and invocation, not the handler's resources.
type httpDispatchRequest struct {
	Route                       uint32
	Method, Path, Header, Token string
	Public                      bool
}
type httpDispatchPlan struct {
	Route  uint32
	Status int
	JSON   bool
}
type httpDispatchResponse struct {
	Status int
	Header http.Header
	Body   []byte
}
type httpDispatchEffects interface {
	Unauthorized()
	JSONHeader()
	Invoke(uint32)
}

func runGoHTTPDispatch(q httpDispatchRequest) httpDispatchPlan {
	if !q.Public && q.Header != "Bearer "+q.Token {
		return httpDispatchPlan{Status: 401}
	}
	return httpDispatchPlan{Route: q.Route, JSON: !q.Public}
}

// Planning is pure and may fall back. Execution never replans or replays a
// handler, including when it panics after committing a response or mutation.
func executeHTTPDispatch(p httpDispatchPlan, e httpDispatchEffects) {
	if p.Status == 401 {
		e.Unauthorized()
		return
	}
	if p.JSON {
		e.JSONHeader()
	}
	e.Invoke(p.Route)
}

type appHTTPDispatchEffects struct {
	w    http.ResponseWriter
	r    *http.Request
	next http.HandlerFunc
}

func (e *appHTTPDispatchEffects) Unauthorized() {
	http.Error(e.w, "unauthorized", http.StatusUnauthorized)
}
func (e *appHTTPDispatchEffects) JSONHeader() {
	e.w.Header().Set("Content-Type", "application/json; charset=utf-8")
}
func (e *appHTTPDispatchEffects) Invoke(_ uint32) { e.next(e.w, e.r) }
func httpDispatchWrapper(pattern, token string, route uint32, public bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		method, path, _ := strings.Cut(pattern, " ")
		// Use the registered canonical path, not URL.Path: ServeMux has already
		// resolved segment escaping. HEAD intentionally matches a GET registration.
		if r.Method == "HEAD" && method == "GET" {
			method = "HEAD"
		}
		q := httpDispatchRequest{route, method, path, r.Header.Get("Authorization"), token, public}
		executeHTTPDispatch(runSelectedHTTPDispatch(q), &appHTTPDispatchEffects{w, r, next})
	}
}
