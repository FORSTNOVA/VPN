package main

import (
	"encoding/binary"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type policyDnsOwnedWriter struct {
	*httptest.ResponseRecorder
	a      *app
	t      *testing.T
	crash  bool
	writes int
}

func (w *policyDnsOwnedWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("policy response outside lock")
	}
	if w.crash {
		panic("synthetic writer")
	}
	return w.ResponseRecorder.Write(b)
}
func (w *policyDnsOwnedWriter) WriteHeader(n int) {
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("policy header outside lock")
	}
	w.ResponseRecorder.WriteHeader(n)
}
func TestPolicyDnsOwnedHTTPResponseAndWriterPanic(t *testing.T) {
	for _, crash := range []bool{false, true} {
		a := settingsDocumentTestApp(t, t.TempDir())
		q := policyDnsRequest{Op: "http"}
		fx := &appPolicyDnsEffects{a: a, q: q}
		w := &policyDnsOwnedWriter{httptest.NewRecorder(), a, t, crash, 0}
		var recovered any
		func() { defer func() { recovered = recover() }(); runPolicyDnsHTTPWithGraph(w, q, fx) }()
		if (recovered != nil) != crash || fx.owned || w.writes != 1 {
			t.Fatal("response or release changed")
		}
		if !a.mu.TryLock() {
			t.Fatal("mutex leaked")
		}
		a.mu.Unlock()
	}
}

type policyDnsCountingFX struct {
	base  *appPolicyDnsEffects
	calls int
}

func (f *policyDnsCountingFX) Call(e policyDnsEffect) policyDnsReply {
	f.calls++
	return f.base.Call(e)
}
func (f *policyDnsCountingFX) Release() { f.base.Release() }

type policyDnsFaultSession struct{ advanced, uncertain, closed bool }

func (s *policyDnsFaultSession) next() ([]byte, uint64, error) {
	if s.advanced {
		return nil, 0, errors.New("synthetic next")
	}
	b := tunReportBytes(tunReportBytes(tunReportBytes(nil, "source"), "rules"), "")
	b = binary.LittleEndian.AppendUint64(b, 0)
	return tunReportBytes(b, ""), 1, nil
}
func (s *policyDnsFaultSession) advance(uint64, []byte) error {
	s.advanced = true
	if s.uncertain {
		return errors.New("uncertain ack")
	}
	return nil
}
func (s *policyDnsFaultSession) close() { s.closed = true }
func TestPolicyDnsContinuationSourceAppendExactlyOnce(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		backing := []string{"DOMAIN,a.example,DIRECT", "untouched"}
		p := sourcePolicy{Rules: backing[:1], MergedRules: []string{"DOMAIN,b.example,REJECT"}}
		q := policyDnsRequest{Op: "rules"}
		fx := &policyDnsCountingFX{base: &appPolicyDnsEffects{q: q, policy: p}}
		session := &policyDnsFaultSession{uncertain: uncertain}
		got, used := runRustPolicyDnsWithStart(q, fx, func([]byte) (policyDnsSession, error) { return session, nil })
		if !used || !session.closed || fx.calls != 1 || backing[1] != p.MergedRules[0] || !reflect.DeepEqual(got.Data["active"], []string{"DOMAIN,a.example,DIRECT", "DOMAIN,b.example,REJECT"}) {
			t.Fatal("source side effect replayed or alias changed")
		}
	}
}
func TestPolicyDnsServiceUnicodeCodecFacts(t *testing.T) {
	for _, v := range []struct{ raw, want string }{{"İ.Example", "+.i.example"}, {"K.Example", "+.k.example"}, {"\u0085NODE.EXAMPLE\u00a0", "+.node.example"}} {
		got, ok := hostnameSuffix(v.raw)
		if !ok || got != v.want {
			t.Fatalf("%q => %q %v", v.raw, got, ok)
		}
	}
	body := []byte("proxies:\n - {server: İ.Example}\ndns:\n nameserver-policy:\n  i.example: https://resolver.example/dns\n")
	if !reflect.DeepEqual(entryDNS(body), map[string][]string{"i.example": {"https://resolver.example/dns"}}) {
		t.Fatal("Go simple lower not retained")
	}
}
func TestPolicyDnsOwnedHTTPReadBranches(t *testing.T) {
	for _, mode := range []string{"ok", "first-read", "zero-nodes", "source-read", "mismatch", "second-read", "bad-yaml"} {
		t.Run(mode, func(t *testing.T) {
			a := settingsDocumentTestApp(t, t.TempDir())
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/profile"; s.ImportPolicy = true })
			q := policyDnsRequest{Op: "http"}
			fx := &appPolicyDnsEffects{a: a, q: q}
			reads := 0
			body := []byte("proxies:\n - {name: node, type: ss, server: node.example, port: 443, cipher: aes-128-gcm, password: synthetic}\nproxy-groups:\n - {name: DirectGroup, type: select, proxies: [DIRECT]}\nrules:\n - DOMAIN,example.com,DirectGroup\n")
			fx.readFile = func(path string) ([]byte, error) {
				reads++
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("read escaped mutex")
				}
				switch reads {
				case 1:
					if path != a.profilePath() {
						t.Fatal("first path")
					}
					if mode == "first-read" {
						return nil, errors.New("synthetic")
					}
					if mode == "zero-nodes" {
						return []byte("proxies: []"), nil
					}
					return body, nil
				case 2:
					if path != a.profilePath()+".source" {
						t.Fatal("source path")
					}
					if mode == "source-read" {
						return nil, errors.New("synthetic")
					}
					if mode == "mismatch" {
						return []byte("wrong"), nil
					}
					return []byte(subscriptionHash("https://synthetic.invalid/profile")), nil
				case 3:
					if path != a.profilePath() {
						t.Fatal("second profile path")
					}
					if mode == "second-read" {
						return nil, errors.New("synthetic")
					}
					if mode == "bad-yaml" {
						return []byte("rules: ["), nil
					}
					return body, nil
				}
				t.Fatal("read replayed")
				return nil, nil
			}
			w := &policyDnsOwnedWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t}
			runPolicyDnsHTTPWithGraph(w, q, fx)
			expected := 3
			if mode == "first-read" || mode == "zero-nodes" {
				expected = 1
			} else if mode == "source-read" || mode == "mismatch" {
				expected = 2
			}
			if reads != expected || fx.owned {
				t.Fatal("read or ownership order")
			}
			wantStatus := 200
			if mode == "second-read" || mode == "bad-yaml" {
				wantStatus = 400
			}
			if w.Code != wantStatus {
				t.Fatalf("status %d", w.Code)
			}
			if mode == "ok" && !strings.Contains(w.Body.String(), "DOMAIN,example.com,DIRECT") {
				t.Fatal("policy output omitted")
			}
			if !a.mu.TryLock() {
				t.Fatal("lock leaked")
			}
			a.mu.Unlock()
		})
	}
}
