package main

import (
	"errors"
	"net"
	"testing"
	"time"
)

type admissionFakeConn struct{ closed int }

func (c *admissionFakeConn) Read([]byte) (int, error)         { return 0, errors.New("unused") }
func (c *admissionFakeConn) Write([]byte) (int, error)        { return 0, errors.New("unused") }
func (c *admissionFakeConn) Close() error                     { c.closed++; return nil }
func (c *admissionFakeConn) LocalAddr() net.Addr              { return nil }
func (c *admissionFakeConn) RemoteAddr() net.Addr             { return nil }
func (c *admissionFakeConn) SetDeadline(time.Time) error      { return nil }
func (c *admissionFakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *admissionFakeConn) SetWriteDeadline(time.Time) error { return nil }
func TestKernelAdmissionServiceSafeReady(t *testing.T) {
	a := &app{mixedPort: 12001}
	q := kernelAdmissionRequest{Op: "ready", Timeout: time.Second}
	e := newKernelAdmissionEffects(a, q)
	now := time.Unix(10, 0)
	e.clock = func() time.Time { return now }
	calls, sleeps := 0, 0
	c := &admissionFakeConn{}
	e.dial = func(network, addr string, d time.Duration) (net.Conn, error) {
		calls++
		if network != "tcp" || d != 300*time.Millisecond {
			t.Fatal("dial contract")
		}
		if calls == 1 {
			if addr != "127.0.0.1:12001" {
				t.Fatal(addr)
			}
			now = now.Add(400 * time.Millisecond)
			a.mixedPort = 12002
			return nil, errors.New("fake")
		}
		if addr != "127.0.0.1:12002" {
			t.Fatal("port captured early")
		}
		now = now.Add(2 * time.Second)
		return c, nil
	}
	e.sleep = func(d time.Duration) {
		if d != 50*time.Millisecond {
			t.Fatal(d)
		}
		sleeps++
		now = now.Add(d)
	}
	r := runGoKernelAdmissionService(q, e)
	if r.Err != nil || calls != 2 || sleeps != 1 || c.closed != 1 {
		t.Fatalf("r=%v calls=%d sleeps=%d close=%d", r, calls, sleeps, c.closed)
	}
	q.Timeout = 0
	e = newKernelAdmissionEffects(a, q)
	e.clock = func() time.Time { return now }
	e.dial = func(string, string, time.Duration) (net.Conn, error) { t.Fatal("expired dial"); return nil, nil }
	r = runGoKernelAdmissionService(q, e)
	if r.Err == nil || r.Err.Error() != "startup timeout" {
		t.Fatal(r)
	}
}
func TestKernelAdmissionServiceSafeInfrastructureFailure(t *testing.T) {
	q := kernelAdmissionRequest{Op: "stop"}
	e := newKernelAdmissionEffects(nil, q)
	e.hooks = map[string]func(kernelAdmissionEffect) kernelAdmissionReply{"running": func(kernelAdmissionEffect) kernelAdmissionReply {
		return kernelAdmissionReply{Err: errors.New("authority unavailable")}
	}}
	e.platform = func(kernelAdmissionEffect) kernelAdmissionReply {
		t.Fatal("continued after infrastructure error")
		return kernelAdmissionReply{}
	}
	r := runKernelAdmissionWithEffects(q, e)
	if r.Err == nil {
		t.Fatal("error silently became stopped")
	}
}
