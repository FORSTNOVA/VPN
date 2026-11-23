package main

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestChinaIntegrationNarrowStatusDoesNotOverwriteOtherDomains(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.setChinaActiveLocked(true)
	a.chinaCommandLocked(serviceChinaCommand{Action: 3, Flag: true})
	a.chinaCommandLocked(serviceChinaCommand{Action: 4, Flag: true})
	a.patchControlLocked(controlSelectedNode, serviceControlState{SelectedNode: "keep"})
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: true})
	a.chinaCommandLocked(serviceChinaCommand{Action: 1, Status: chinaIPStatus{Available: true}})
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: false})
	c := a.chinaStateLocked()
	if !c.Active || !c.Probed || !c.RuleOK || c.Fetching || !c.Status.Available || a.controlMetaLocked().SelectedNode != "keep" {
		t.Fatalf("narrow completion overwrote state %#v", c)
	}
	s := settingsStateFromApp(a)
	a.setChinaActiveLocked(false)
	applySettingsServiceState(a, s)
	if a.chinaStateLocked().Active {
		t.Fatal("settings snapshot restored china state")
	}
}
func TestChinaIntegrationStatusIsCopyWithLiveFlags(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.setChinaActiveLocked(true)
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: true})
	a.chinaCommandLocked(serviceChinaCommand{Action: 1, Status: chinaIPStatus{Available: true}})
	v := a.chinaIPStatusLocked()
	if !v.Active || !v.Refreshing || !v.Available {
		t.Fatal("status flags absent")
	}
	v.Active = false
	v.Refreshing = false
	v.Available = false
	live := a.chinaStateLocked()
	if !live.Active || !live.Fetching || !live.Status.Available || live.Status.Active || live.Status.Refreshing {
		t.Fatal("status copy mutated authority")
	}
}
func TestChinaIntegrationNilKernelStopKeepsActiveQuirk(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	a.setChinaActiveLocked(true)
	if out := a.runKernelService(kernelServiceRequest{Op: "stop"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if !a.chinaStateLocked().Active {
		t.Fatal("nil-kernel stop changed original active quirk")
	}
}
func TestChinaIntegrationKernelActiveBeforeRunnerFailure(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	f := kernelFixture()
	f.Options = kernelOptionFacts{ChinaDirect: true}
	f.Fail = map[string]error{"runner-new": errors.New("synthetic")}
	fx := &chinaIntegrationKernelEffects{a: a, f: f}
	out := runSelectedKernelService(kernelServiceRequest{Op: "start"}, fx)
	if out.Err == nil || !a.chinaStateLocked().Active || !fx.runnerSawActive {
		t.Fatal("active no longer committed before runner creation")
	}
}

type chinaIntegrationKernelEffects struct {
	a               *app
	f               *kernelMemoryEffects
	runnerSawActive bool
}

func (f *chinaIntegrationKernelEffects) Release() { f.f.Release() }
func (f *chinaIntegrationKernelEffects) Call(q kernelEffect) kernelReply {
	if q.Kind == "china-active" {
		f.a.setChinaActiveLocked(q.Bool)
	}
	if q.Kind == "runner-new" {
		f.runnerSawActive = f.a.chinaStateLocked().Active
	}
	return f.f.Call(q)
}
func TestChinaIntegrationClosedStateFailsBeforePlatform(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	if e := a.service.Close(); e != nil {
		t.Fatal(e)
	}
	a.proxyStateHook = func() (bool, string, error) { t.Fatal("platform hook reached"); return false, "", nil }
	w := httptest.NewRecorder()
	a.state(w, httptest.NewRequest("GET", "/state", nil))
	if w.Code != 500 {
		t.Fatalf("wanted 500, got %d", w.Code)
	}
	if !a.mu.TryLock() {
		t.Fatal("state leaked mutex")
	}
	a.mu.Unlock()
}
