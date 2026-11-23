package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type appKernelAdmissionEffects struct {
	a        *app
	q        kernelAdmissionRequest
	hooks    map[string]func(kernelAdmissionEffect) kernelAdmissionReply
	platform func(kernelAdmissionEffect) kernelAdmissionReply
	cleanup  func()
	clock    func() time.Time
	sleep    func(time.Duration)
	dial     func(string, string, time.Duration) (net.Conn, error)
	deadline time.Time
	conn     net.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	lastErr  error
	output   []byte
	rows     []string
}

func newKernelAdmissionEffects(a *app, q kernelAdmissionRequest) *appKernelAdmissionEffects {
	return &appKernelAdmissionEffects{a: a, q: q, clock: time.Now, sleep: time.Sleep, dial: net.DialTimeout}
}
func (e *appKernelAdmissionEffects) closeConn() {
	if e.conn != nil {
		c := e.conn
		e.conn = nil
		_ = c.Close()
	}
}
func (e *appKernelAdmissionEffects) cancelContext() {
	if e.cancel != nil {
		c := e.cancel
		e.cancel = nil
		c()
	}
}
func (e *appKernelAdmissionEffects) Release() {
	defer e.cancelContext()
	defer e.closeConn()
	if e.cleanup != nil {
		e.cleanup()
	}
}
func (e *appKernelAdmissionEffects) Call(q kernelAdmissionEffect) (r kernelAdmissionReply) {
	defer func() {
		if r.Err != nil {
			e.lastErr = r.Err
		}
	}()
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	switch q.Kind {
	case "saved-path":
		r.Text = strings.TrimSpace(e.a.controlMetaLocked().MihomoPath)
	case "bundle-present":
		r.Bool = e.a.bundleDir != ""
	case "bundle-path":
		r.Text = filepath.Join(e.a.bundleDir, q.Text)
	case "home-path":
		r.Text = filepath.Join(e.a.home, q.Text)
	case "stat":
		v, err := os.Stat(q.Text)
		r.Err = err
		if err == nil {
			r.Bool = v.IsDir()
		}
	case "deadline":
		e.deadline = e.clock().Add(q.Duration)
	case "before":
		r.Bool = e.clock().Before(e.deadline)
	case "dial":
		e.conn, r.Err = e.dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.a.mixedPort)), q.Duration)
	case "conn-close":
		e.closeConn()
	case "sleep":
		e.sleep(q.Duration)
	case "context":
		e.ctx, e.cancel = context.WithTimeout(context.Background(), q.Duration)
	case "context-cancel":
		e.cancelContext()
	case "owner-address":
		r.Text = "127.0.0.1:" + strconv.Itoa(q.Index)
	case "row-count":
		e.rows = strings.Split(string(e.output), "\n")
		r.Count = len(e.rows)
	case "row":
		r.Names = strings.Fields(e.rows[q.Index])
	case "output-split":
		r.Names = strings.Split(strings.TrimSpace(string(e.output)), "\n")
	case "error":
		r.Err = kernelAdmissionError(q.Index, q.Text, e.q.Port, e.lastErr)
	default:
		if e.platform == nil {
			panic("unavailable kernel admission platform effect")
		}
		return e.platform(q)
	}
	return
}
func runKernelAdmissionWithEffects(q kernelAdmissionRequest, e kernelAdmissionEffects) (r kernelAdmissionResult) {
	defer func() {
		if v := recover(); v != nil {
			r = kernelAdmissionResult{Err: fmt.Errorf("kernel admission interrupted: %v", v)}
		}
	}()
	return runSelectedKernelAdmissionService(q, e)
}
func (a *app) runKernelAdmission(q kernelAdmissionRequest) kernelAdmissionResult {
	return runKernelAdmissionWithEffects(q, newKernelAdmissionEffects(a, q))
}
