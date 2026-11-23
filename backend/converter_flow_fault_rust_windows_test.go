//go:build windows && amd64 && rustcore

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

type converterFaultFixture struct {
	op, body, name string
	sources        []SubscriptionSource
}

func converterFaultFixtures() []converterFaultFixture {
	clash := "proxies:\n  - {name: A, type: ss, server: fake.invalid, port: 1, cipher: fake, password: fake}\n"
	sing := `{"outbounds":[{"type":"ss","tag":"S","server":"fake.invalid","server_port":1,"method":"fake","password":"fake"},{"type":"vmess","tag":"V","uuid":"fake","transport":{"type":"ws","path":"/fake","headers":{"Host":"fake.invalid"}},"tls":{"enabled":true,"server_name":"fake.invalid","insecure":true,"alpn":["h2"]}},{"type":"vless","tag":"L","uuid":"fake","flow":"fake","tls":{"enabled":true,"reality":{"enabled":true,"public_key":"fake","short_id":"fake"}}},{"type":"trojan","tag":"T","password":"fake"},{"type":"hysteria2","tag":"H","password":"fake","obfs":{"type":"fake","password":"fake"}},{"type":"tuic","tag":"U","uuid":"fake","congestion_controller":"fake"}]}`
	vm := base64.StdEncoding.EncodeToString([]byte(`{"ps":"vm","add":"fake.invalid","port":"1","id":"fake","net":"ws","tls":"tls","path":"/fake","host":"fake.invalid"}`))
	return []converterFaultFixture{
		{op: "singbox", body: sing}, {op: "singbox", body: `{"outbounds":[{"type":"unknown","tag":"skip"}]}`},
		{op: "parse-body", body: clash}, {op: "parse-body", body: sing}, {op: "parse-body", body: base64.StdEncoding.EncodeToString([]byte(clash))},
		{op: "parse-body", body: "vmess://" + vm + "\nvless://fake@fake.invalid:1#uri\nunknown://fake"},
		{op: "parse-body", body: "invalid"}, {op: "ensure-yaml", body: clash}, {op: "ensure-yaml", body: base64.StdEncoding.EncodeToString([]byte(clash))}, {op: "ensure-yaml", body: sing},
		{op: "merge-sources", sources: []SubscriptionSource{{Label: "X", Prefix: "P", Body: []byte(clash + "proxy-groups:\n - {name: G, type: select, proxies: [A, DIRECT]}\nrules: [\"DOMAIN,fake.invalid,G,no-resolve\"]\ndns:\n nameserver-policy: {fake.invalid: [fake]}\n")}, {Label: "X", Body: []byte(clash)}, {Body: []byte("invalid")}}},
		{op: "unique-name", name: "A"},
	}
}

type converterTrackedFX struct {
	*appConverterEffects
	trace     []converterServiceEffect
	release   int
	panicKind string
	panicked  bool
}

func (f *converterTrackedFX) Call(e converterServiceEffect) converterServiceReply {
	f.trace = append(f.trace, cloneConverterEffect(e))
	r := f.appConverterEffects.Call(e)
	if e.Kind == f.panicKind && !f.panicked {
		f.panicked = true
		panic("after converter effect")
	}
	return r
}
func (f *converterTrackedFX) Release() { f.release++; f.appConverterEffects.Release() }
func converterMakeFixture(c converterFaultFixture) (converterServiceRequest, *converterTrackedFX) {
	e := newConverterEffects()
	q := converterServiceRequest{Op: c.op, Name: c.name}
	if c.op != "unique-name" && c.op != "merge-sources" {
		q.Body = e.put([]byte(c.body))
	}
	for _, s := range c.sources {
		q.Sources = append(q.Sources, converterSource{Body: e.put(s.Body), Label: s.Label, Prefix: s.Prefix})
	}
	if c.op == "unique-name" {
		e.used = map[string]struct{}{"A": {}, "A (2)": {}}
	}
	return q, &converterTrackedFX{appConverterEffects: e}
}
func converterProjected(e *appConverterEffects, r converterServiceResult) []any {
	v := []any{r.Text}
	if r.Err != nil {
		v = append(v, r.Err.Error())
	} else {
		v = append(v, nil)
	}
	for _, t := range []int{r.Body, r.Proxies, r.Nodes} {
		if t == 0 {
			v = append(v, nil)
		} else {
			v = append(v, e.get(t))
		}
	}
	return v
}

type converterFaultSession struct {
	converterFlowSession
	kind         string
	afterNext    bool
	armed, fired bool
	last         converterFrame
}

func (s *converterFaultSession) next() ([]byte, uint64, error) {
	if s.armed {
		s.armed = false
		return nil, 0, errors.New("fault next")
	}
	b, n, e := s.converterFlowSession.next()
	if e == nil {
		s.last, _ = decodeConverterFrame(b)
	}
	return b, n, e
}
func (s *converterFaultSession) advance(n uint64, b []byte) error {
	e := s.converterFlowSession.advance(n, b)
	if e == nil && !s.fired && s.last.kind == s.kind {
		s.fired = true
		if s.afterNext {
			s.armed = true
		} else {
			return errors.New("fault advance")
		}
	}
	return e
}
func TestConverterFlowActualBoundaryFaults(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	cases := converterFaultFixtures()
	seen := map[string]int{}
	for i, c := range cases {
		q, f := converterMakeFixture(c)
		runGoConverterService(q, f)
		for _, e := range f.trace {
			if _, ok := seen[e.Kind]; !ok {
				seen[e.Kind] = i
			}
		}
	}
	for k, i := range seen {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/next=%v", k, next), func(t *testing.T) {
				c := cases[i]
				q, w := converterMakeFixture(c)
				want := runGoConverterService(q, w)
				q, g := converterMakeFixture(c)
				var injected *converterFaultSession
				got, used := runRustConverterServiceWithStart(q, g, func(b []byte) (converterFlowSession, error) {
					s, e := startRustConverterFlow(b)
					injected = &converterFaultSession{converterFlowSession: s, kind: k, afterNext: next}
					return injected, e
				})
				if !used || injected == nil || !injected.fired || !reflect.DeepEqual(converterProjected(w.appConverterEffects, want), converterProjected(g.appConverterEffects, got)) || !reflect.DeepEqual(w.trace, g.trace) || g.release != 1 {
					t.Fatalf("fault %s used=%v fired=%v result=%#v want=%#v trace=%d/%d release=%d", k, used, injected != nil && injected.fired, got, want, len(g.trace), len(w.trace), g.release)
				}
			})
		}
	}
	t.Logf("%d distinct effect boundaries, %d injections", len(seen), len(seen)*2)
}
func TestConverterFlowActualAfterEffectPanic(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	seen := map[string]converterFaultFixture{}
	for _, c := range converterFaultFixtures() {
		q, f := converterMakeFixture(c)
		runGoConverterService(q, f)
		for _, e := range f.trace {
			if _, ok := seen[e.Kind]; !ok {
				seen[e.Kind] = c
			}
		}
	}
	for k, c := range seen {
		t.Run(k, func(t *testing.T) {
			q, f := converterMakeFixture(c)
			f.panicKind = k
			r, used := runRustConverterService(q, f)
			if !used || r.Err == nil || !f.panicked || f.release != 1 {
				t.Fatalf("used=%v err=%v panic=%v release=%d", used, r.Err, f.panicked, f.release)
			}
			if f.trace[len(f.trace)-1].Kind != k {
				t.Fatal("continued after unknown panic")
			}
		})
	}
}

type converterOversizeFX struct{ calls, released int }

func (f *converterOversizeFX) Call(e converterServiceEffect) converterServiceReply {
	f.calls++
	switch e.Kind {
	case "used-name":
		return converterServiceReply{Text: strings.Repeat("x", (8<<20)+1)}
	case "use-name":
		return converterServiceReply{}
	default:
		panic("unexpected oversize port")
	}
}
func (f *converterOversizeFX) Release() { f.released++ }
func TestConverterFlowActualOversizeContinuation(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	f := &converterOversizeFX{}
	r, used := runRustConverterService(converterServiceRequest{Op: "unique-name", Name: "A"}, f)
	if !used || r.Err != nil || r.Text != "A" || f.calls != 2 || f.released != 1 {
		t.Fatalf("result=%#v used=%v calls=%d release=%d", r, used, f.calls, f.released)
	}
}
func TestConverterFlowActualCapacityCancel(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	b, e := converterStartInput(converterServiceRequest{Op: "unique-name", Name: "A"})
	if e != nil {
		t.Fatal(e)
	}
	var ss []converterFlowSession
	defer func() {
		for _, s := range ss {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustConverterFlow(b)
		if e != nil {
			t.Fatal(i, e)
		}
		ss = append(ss, s)
	}
	q, f := converterMakeFixture(converterFaultFixture{op: "unique-name", name: "A"})
	_, used := runRustConverterService(q, f)
	if used || len(f.trace) != 0 || f.release != 0 {
		t.Fatal("capacity fallback touched resources")
	}
	for _, s := range ss {
		s.close()
	}
	ss = nil
	n := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newConverterMirror(q)
		m.close()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if runtime.NumGoroutine() > n+4 {
		t.Fatal("cancelled converter mirror leaked")
	}
}

type converterMalformedSession struct {
	converterFlowSession
	target     string
	mode       int
	arm, fired bool
	last       converterFrame
}

func (s *converterMalformedSession) next() ([]byte, uint64, error) {
	b, n, e := s.converterFlowSession.next()
	if e != nil {
		return b, n, e
	}
	s.last, _ = decodeConverterFrame(b)
	if s.arm && !s.fired {
		s.fired = true
		s.arm = false
		switch s.mode {
		case 0:
			b = b[:len(b)-1]
		case 1:
			b = append(b, 0)
		case 2:
			n++
		case 3:
			b = append([]byte{}, b...)
			b[0] = 255
		}
	}
	return b, n, nil
}
func (s *converterMalformedSession) advance(n uint64, b []byte) error {
	e := s.converterFlowSession.advance(n, b)
	if e == nil && s.last.kind == s.target {
		s.arm = true
	}
	return e
}
func TestConverterFlowActualMalformedAfterMutation(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	for _, c := range converterFaultFixtures() {
		if c.op != "merge-sources" && c.op != "singbox" {
			continue
		}
		q, w := converterMakeFixture(c)
		want := runGoConverterService(q, w)
		kinds := map[string]bool{}
		for _, e := range w.trace {
			switch e.Kind {
			case "map-new", "map-assign", "use-name", "node-new", "transport-child":
				kinds[e.Kind] = true
			}
		}
		for k := range kinds {
			for mode := 0; mode < 4; mode++ {
				t.Run(fmt.Sprintf("%s/%s/%d", c.op, k, mode), func(t *testing.T) {
					q, g := converterMakeFixture(c)
					var s *converterMalformedSession
					r, used := runRustConverterServiceWithStart(q, g, func(b []byte) (converterFlowSession, error) {
						x, e := startRustConverterFlow(b)
						s = &converterMalformedSession{converterFlowSession: x, target: k, mode: mode}
						return s, e
					})
					if !used || s == nil || !s.fired || !reflect.DeepEqual(w.trace, g.trace) || !reflect.DeepEqual(converterProjected(w.appConverterEffects, want), converterProjected(g.appConverterEffects, r)) || g.release != 1 {
						t.Fatalf("mutation malformed mode=%d kind=%s used=%v fired=%v result=%#v", mode, k, used, s != nil && s.fired, r)
					}
				})
			}
		}
	}
}

func TestConverterFlowWire(t *testing.T) {
	q := converterServiceRequest{Op: "merge-sources", Sources: []converterSource{{Body: 11, Label: string([]byte{255, 'A'}), Prefix: "P"}}}
	b, e := converterStartInput(q)
	if e != nil || len(b) == 0 {
		t.Fatal(e)
	}
	r := converterServiceReply{Tokens: []int{}, Fields: []converterField{{Key: "password", Ref: 19, TrimRef: 20, Empty: false, TrimEmpty: false}}, Names: []string{}}
	c := cloneConverterReply(r)
	c.Fields[0].Ref = 88
	if r.Fields[0].Ref != 19 || c.Tokens == nil || c.Names == nil {
		t.Fatal("mirror metadata clone lost shape/isolation")
	}
	a := newConverterArena(q)
	err := errors.New("opaque error with synthetic secret")
	encoded, e := a.replyBytes(converterFrame{kind: "decode-base"}, converterServiceReply{Err: err})
	if e != nil || strings.Contains(string(encoded), err.Error()) {
		t.Fatal("opaque error leaked to native")
	}
	f := converterFrame{errorCode: 1, errorToken: a.errorKinds["decode-base"]}
	if !a.resultMatches(f, converterServiceResult{Err: err}) || a.resultMatches(f, converterServiceResult{Err: errors.New(err.Error())}) {
		t.Fatal("error resource identity not retained")
	}
	for i := 0; i < 10000; i++ {
		_, e = a.replyBytes(converterFrame{kind: "decode-base"}, converterServiceReply{Err: err})
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(a.errs) != 1 || len(a.errorKinds) != 1 {
		t.Fatal("error arena grew per iteration")
	}
}

type converterMarshalFailureFX struct {
	*converterTrackedFX
	kind string
}

func (f *converterMarshalFailureFX) Call(e converterServiceEffect) converterServiceReply {
	if e.Kind == f.kind {
		f.trace = append(f.trace, cloneConverterEffect(e))
		return converterServiceReply{Err: errors.New("synthetic marshal failure")}
	}
	return f.converterTrackedFX.Call(e)
}
func TestConverterFlowActualMarshalFailurePreservesMutation(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	for _, c := range converterFaultFixtures() {
		if c.op != "ensure-yaml" && c.op != "merge-sources" {
			continue
		}
		kind := "marshal-proxies"
		if c.op == "merge-sources" {
			kind = "merge-marshal"
		}
		q, w := converterMakeFixture(c)
		wf := &converterMarshalFailureFX{w, kind}
		want := runGoConverterService(q, wf)
		found := false
		for _, e := range w.trace {
			if e.Kind == kind {
				found = true
			}
		}
		if !found {
			continue
		}
		q, g := converterMakeFixture(c)
		gf := &converterMarshalFailureFX{g, kind}
		var injected *converterFaultSession
		got, used := runRustConverterServiceWithStart(q, gf, func(b []byte) (converterFlowSession, error) {
			s, e := startRustConverterFlow(b)
			injected = &converterFaultSession{converterFlowSession: s, kind: kind}
			return injected, e
		})
		if !used || injected == nil || !injected.fired || got.Err == nil || want.Err == nil || got.Err.Error() != want.Err.Error() || !reflect.DeepEqual(w.trace, g.trace) || !reflect.DeepEqual(w.arena, g.arena) {
			t.Fatalf("marshal failure lost partial graph op=%s got=%#v want=%#v fired=%v", c.op, got, want, injected != nil && injected.fired)
		}
	}
}

func TestConverterFlowActualOriginalGraph(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	converterSafeOriginalGraph(t, func(q converterServiceRequest, fx converterServiceEffects) converterServiceResult {
		before := converterFlowRecoveries.Load()
		r, used := runRustConverterService(q, fx)
		if !used || converterFlowRecoveries.Load() != before {
			t.Fatalf("graph did not run strictly native: used=%v reason=%v", used, converterFlowRecoveryReason.Load())
		}
		return r
	})
	body := []byte("# preserved\nproxies:\n - {name: N, type: ss, server: fake.invalid, port: 1}\n")
	q, e := converterBodyRequest("ensure-yaml", body)
	before := converterFlowRecoveries.Load()
	r, used := runRustConverterService(q, e)
	b, _, err := converterByteResult(e, r)
	if !used || converterFlowRecoveries.Load() != before || err != nil || len(b) != len(body) || &b[0] != &body[0] {
		t.Fatal("native ensure changed byte identity", used, err)
	}
}

func TestConverterFlowActualLongFiniteNaming(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter DLL unavailable")
	}
	e := newConverterEffects()
	e.used = map[string]struct{}{"A": {}}
	for i := 2; i <= 100001; i++ {
		e.used[fmt.Sprintf("A (%d)", i)] = struct{}{}
	}
	f := newConverterSafeEffects(e)
	before := converterFlowRecoveries.Load()
	r, used := runRustConverterService(converterServiceRequest{Op: "unique-name", Name: "A"}, f)
	if !used || r.Err != nil || r.Text != "A (100002)" || f.calls["used-name"] != 100002 || f.calls["use-name"] != 1 || converterFlowRecoveries.Load() != before || len(e.arena) != 1 || f.releases != 1 {
		t.Fatalf("long finite name r=%#v used=%v calls=%v recovery=%d arena=%d", r, used, f.calls, converterFlowRecoveries.Load()-before, len(e.arena))
	}
}

func TestConverterFlowActualUnicodeLower(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter v2 DLL unavailable")
	}
	beforeChild := nodeFrontRecoveries.Load()
	converterSafeUnicodeLower(t, func(q converterServiceRequest, fx converterServiceEffects) converterServiceResult {
		before := converterFlowRecoveries.Load()
		r, used := runRustConverterService(q, fx)
		if !used || converterFlowRecoveries.Load() != before {
			t.Fatalf("Unicode normal recovery used=%v reason=%v", used, converterFlowRecoveryReason.Load())
		}
		return r
	})
	if nodeFrontRecoveries.Load() != beforeChild {
		t.Fatal("Unicode child planner recovered")
	}
}

func TestConverterFlowOldVersionFallback(t *testing.T) {
	if os.Getenv("SMARTVPN_TEST_CONVERTER_OLD_DLL") != "1" {
		t.Skip("explicit old converter DLL compatibility test")
	}
	if !rustCoreAvailable() {
		t.Fatal("old core DLL unavailable")
	}
	version, err := windows.GetProcAddress(rustCore.handle, "smartvpn_converter_flow_version_v1")
	if err != nil {
		t.Fatal(err)
	}
	v, _, _ := syscall.SyscallN(version)
	if v != 1 {
		t.Fatalf("requires old v1 converter, got %d", v)
	}
	if rustConverterFlowAvailable() {
		t.Fatal("v1 frame accepted by v2 bridge")
	}
	q, e := converterMakeFixture(converterFaultFixture{op: "unique-name", name: "A"})
	_, used := runRustConverterService(q, e)
	if used || len(e.trace) != 0 || e.release != 0 {
		t.Fatal("incompatible DLL executed effect or claimed resource")
	}
	r := runSelectedConverterService(q, e)
	if r.Err != nil || r.Text != "A (3)" || len(e.trace) != 4 || e.release != 1 {
		t.Fatalf("old DLL whole-Go fallback failed: r=%#v effects=%d release=%d", r, len(e.trace), e.release)
	}
}
