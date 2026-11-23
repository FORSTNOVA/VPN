package main

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

type chinaTestSpec struct {
	Override     bool
	OverrideBody []byte
	Cache        []byte
	Fetch        map[string][]byte
	Fail         map[string]bool
	Age          time.Duration
	Options      configOptions
	Import       bool
	Initial      serviceChinaState
}
type chinaTestFX struct {
	t       *testing.T
	a       *app
	s       chinaTestSpec
	Trace   []chinaServiceEffect
	Owned   bool
	Spawns  []chinaListMode
	Now     time.Time
	options configOptions
}

func chinaTestBase() chinaTestSpec {
	body := chinaListBody(1000)
	return chinaTestSpec{Cache: body, Fetch: map[string][]byte{"m1/china.txt": body, "m1/china6.txt": body, "m2/china.txt": body, "m2/china6.txt": body}, Fail: map[string]bool{}, Age: time.Hour}
}
func chinaTestNew(t *testing.T, s chinaTestSpec, factory authorityFactory) *chinaTestFX {
	a := regionAuthorityNew(t, factory)
	for _, c := range []serviceChinaCommand{{Action: 1, Status: s.Initial.Status}, {Action: 2, Flag: s.Initial.Fetching}, {Action: 3, Flag: s.Initial.Probed}, {Action: 4, Flag: s.Initial.RuleOK}, {Action: 5, Flag: s.Initial.Active}} {
		a.chinaCommandLocked(c)
	}
	return &chinaTestFX{t: t, a: a, s: s, Now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 8*3600)), options: s.Options}
}
func (f *chinaTestFX) Release() {
	if f.Owned {
		f.Owned = false
		f.a.mu.Unlock()
	}
}
func (f *chinaTestFX) Call(q chinaServiceEffect) (r chinaServiceReply) {
	f.Trace = append(f.Trace, q)
	if f.s.Fail[q.Kind] || f.s.Fail[q.Kind+":"+q.Name] {
		r.Err = fmt.Errorf("synthetic %s", q.Kind)
		if q.Kind == "options-import" {
			f.options.SourceRuleOrder = true
			r.Options = f.options
		}
		return
	}
	switch q.Kind {
	case "lines":
		r.Names = strings.Split(string(q.Body), "\n")
		for i := range r.Names {
			r.Names[i] = strings.TrimSpace(r.Names[i])
		}
	case "cidr":
		_, n, e := net.ParseCIDR(q.Name)
		r.Err = e
		if e == nil {
			r.Text = n.String()
		}
	case "fetch-override":
		r.Flag = f.s.Override
		r.Body = f.s.OverrideBody
		r.Text = "override"
	case "sources":
		r.Names = []string{"m1/", "m2/"}
		r.Ranges = []string{"china.txt", "china6.txt"}
	case "fetch":
		var ok bool
		r.Body, ok = f.s.Fetch[q.Name]
		if !ok {
			r.Err = errors.New("synthetic missing source")
		}
	case "stat":
		r.Now = f.Now.Add(-f.s.Age)
	case "read":
		r.Body = f.s.Cache
	case "since":
		r.Flag = f.Now.Sub(q.Time) <= chinaIPListTTL
	case "now":
		r.Now = f.Now
	case "mkdir", "write-temp", "rename", "free-port", "probe-mkdir", "probe-list-write", "probe-config-write", "probe-check", "probe-remove-list", "probe-remove-config", "log":
	case "state":
		r.State = f.a.chinaStateLocked()
	case "command":
		f.a.chinaCommandLocked(serviceChinaCommand{Action: q.Action, Status: q.Status, Flag: q.Flag})
	case "lock":
		if f.Owned {
			panic("duplicate lock")
		}
		f.a.mu.Lock()
		f.Owned = true
	case "unlock":
		if !f.Owned {
			panic("unowned unlock")
		}
		f.Owned = false
		f.a.mu.Unlock()
	case "spawn":
		if f.Owned {
			panic("spawn before unlock")
		}
		f.Spawns = append(f.Spawns, chinaListMode(q.Index))
	case "options-base":
		r.Options = f.options
		r.ImportPolicy = f.s.Import
	case "options-china":
		f.options.ChinaDirect = q.Flag
	case "options-import", "options-chain", "options-domains":
		r.Options = f.options
	default:
		panic("unsafe/unknown China fixture port: " + q.Kind)
	}
	return
}

type chinaTestRun func(chinaServiceRequest, chinaServiceEffects) chinaServiceResult

func chinaTraceKinds(f *chinaTestFX) []string {
	var out []string
	for _, q := range f.Trace {
		if q.Kind != "lines" && q.Kind != "cidr" {
			out = append(out, q.Kind+":"+q.Name)
		}
	}
	return out
}
func chinaTestError(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
func chinaTestParity(t *testing.T, q chinaServiceRequest, s chinaTestSpec, run chinaTestRun, factory authorityFactory) {
	g := chinaTestNew(t, s, func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
	r := chinaTestNew(t, s, factory)
	owner := r.a.service.backend
	want := runGoChinaService(q, g)
	got := run(q, r)
	we, ge := chinaTestError(want.Err), chinaTestError(got.Err)
	want.Err = nil
	got.Err = nil
	if we != ge || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(g.Trace, r.Trace) || !reflect.DeepEqual(g.a.chinaStateLocked(), r.a.chinaStateLocked()) || !reflect.DeepEqual(g.Spawns, r.Spawns) || r.Owned || g.Owned {
		t.Fatalf("China parity op=%s errors=%q/%q kinds=%v/%v", q.Op, we, ge, chinaTraceKinds(g), chinaTraceKinds(r))
	}
	if r.a.service.backend != owner {
		t.Fatal("planner transferred owner")
	}
}
