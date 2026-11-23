//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type pipelineDiffEvent struct {
	Effect profileEffect
	State  profileServiceState
}
type pipelineDiffFX struct {
	UseRustWorkers, ReverseWorkers bool
	WorkerTraces                   map[string][]pipelineDiffEvent
	WorkerProblems                 []string
	Completion                     []string
	DownloadMode                   int
	FailExtra                      map[string]int
	mu                             sync.Mutex
	Files                          map[string][]byte
	Trace                          []pipelineDiffEvent
	Calls                          map[string]int
	FailKind                       string
	FailAt                         int
	FailPath                       string
	Running, ProxyOn               bool
	Proxy                          string
	Now                            int64
	DownloadFails                  int
	DownloadNoNodes                bool
	Nodes                          []proxyNode
}

func pipelineClone(s profileServiceState) profileServiceState {
	o := s
	o.settingsServiceState = settingsDiffClone(s.settingsServiceState)
	o.ManualNodes = settingsDiffCopy(reflect.ValueOf(s.ManualNodes)).Interface().([]manualNode)
	return o
}
func pipelineNodes(body []byte) []proxyNode {
	if len(body) == 0 || string(body) == "invalid" {
		return nil
	}
	return []proxyNode{{Name: "selected"}, {Name: "second"}}
}
func (f *pipelineDiffFX) Call(q profileEffect, s *profileServiceState) (r profileReply) {
	if q.Kind == "acquire-batch" {
		return f.batch(q, s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Trace = append(f.Trace, pipelineDiffEvent{settingsDiffCopy(reflect.ValueOf(q)).Interface().(profileEffect), pipelineClone(*s)})
	if f.Calls == nil {
		f.Calls = map[string]int{}
	}
	f.Calls[q.Kind]++
	if f.FailExtra[q.Kind] > 0 && f.Calls[q.Kind] == f.FailExtra[q.Kind] {
		r.Err = fmt.Errorf("synthetic %s failure", q.Kind)
		return
	}
	if q.Kind == f.FailKind && (f.FailAt == 0 || f.Calls[q.Kind] == f.FailAt) && (f.FailPath == "" || f.FailPath == q.Path) {
		r.Err = fmt.Errorf("synthetic %s failure", q.Kind)
		return
	}
	switch q.Kind {
	case "read":
		var ok bool
		r.Body, ok = f.Files[q.Path]
		if !ok {
			r.Err = os.ErrNotExist
		} else {
			r.Body = append([]byte{}, r.Body...)
		}
	case "write":
		f.Files[q.Path] = append([]byte{}, q.Body...)
	case "rename":
		b, ok := f.Files[q.Path]
		if !ok {
			r.Err = os.ErrNotExist
		} else {
			f.Files[q.OtherPath] = b
			delete(f.Files, q.Path)
		}
	case "remove":
		if _, ok := f.Files[q.Path]; !ok {
			r.Err = os.ErrNotExist
		} else {
			delete(f.Files, q.Path)
		}
	case "mkdir":
	case "parse":
		r.Nodes = pipelineNodes(q.Body)
	case "ensure-yaml":
		r.Body = append([]byte("converted:"), q.Body...)
		r.Nodes = pipelineNodes(q.Body)
	case "merge-profiles", "merge-sources":
		r.Body = []byte("merged")
		r.Nodes = []proxyNode{{Name: "selected"}, {Name: "second"}}
		if f.Nodes != nil {
			r.Nodes = append([]proxyNode{}, f.Nodes...)
		}
	case "running":
		r.Bool = f.Running
	case "proxy-state":
		r.Bool = f.ProxyOn
		r.Text = f.Proxy
	case "download":
		if f.Calls[q.Kind] <= f.DownloadFails {
			if f.DownloadMode == 1 {
				r.Err = fmt.Errorf("wrapped: %w", errNoUsableNodes)
			} else if f.DownloadMode == 2 {
				r.Err = errors.New(errNoUsableNodes.Error())
			} else if f.DownloadMode == 3 {
				r.Err = fmt.Errorf("wrapped: %w", errSubscriptionNetwork)
			} else if f.DownloadMode == 4 {
				r.Err = errors.New(errSubscriptionNetwork.Error())
			} else if f.DownloadNoNodes {
				r.Err = errNoUsableNodes
			} else {
				r.Err = errSubscriptionNetwork
			}
		} else {
			r.Body = []byte("downloaded")
			r.Nodes = []proxyNode{{Name: "selected"}, {Name: "second"}}
			r.Info = &subscriptionUserInfo{Total: 9007199254740993}
		}
	case "prepare-live-config":
		r.Text = "synthetic-live-config"
	case "apply-live-config", "save-settings", "save-nodes", "log":
	case "now":
		r.Timestamp = f.Now
	case "registry":
		fx := subDiffFX{Now: f.Now}
		out := runGoSubscriptionService(&s.settingsServiceState, prepareSubscriptionServiceRequest(q.Registry), &fx)
		r.Err, r.Entry, r.Found = out.Err, out.Entry, out.Found
	default:
		panic("unknown pipeline fake effect " + q.Kind)
	}
	return
}
func pipelineFakeCopy(f *pipelineDiffFX) *pipelineDiffFX {
	return &pipelineDiffFX{UseRustWorkers: f.UseRustWorkers, ReverseWorkers: f.ReverseWorkers, DownloadMode: f.DownloadMode, FailExtra: f.FailExtra, Files: settingsDiffCopy(reflect.ValueOf(f.Files)).Interface().(map[string][]byte), FailKind: f.FailKind, FailAt: f.FailAt, FailPath: f.FailPath, Running: f.Running, ProxyOn: f.ProxyOn, Proxy: f.Proxy, Now: f.Now, DownloadFails: f.DownloadFails, DownloadNoNodes: f.DownloadNoNodes, Nodes: f.Nodes}
}
func pipelineDiffBase() (profileServiceState, *pipelineDiffFX) {
	s := profileServiceState{settingsServiceState: settingsDiffBase(), Home: `X:\synthetic-only`, ProfilePath: `X:\synthetic-only\providers\subscription.yaml`, SelectedChoice: "selected", MixedPort: 7890}
	s.Settings.SelectedNode = "selected"
	s.Subscriptions = []subscriptionEntry{{ID: "fixture", URL: s.Settings.SubscriptionURL, Label: "fixture-label", Enabled: true}}
	f := &pipelineDiffFX{Files: map[string][]byte{s.ProfilePath: []byte("previous"), s.ProfilePath + ".source": []byte(subscriptionHash(s.Settings.SubscriptionURL)), filepath.Join(s.Home, "subscriptions", "fixture.yaml"): []byte("cached"), filepath.Join(s.Home, "subscriptions", "fixture.yaml.format"): []byte("cache-format")}, Now: 1800000000, Proxy: "127.0.0.1:12345"}
	return s, f
}
func pipelineDiffCompare(t *testing.T, s profileServiceState, q profileServiceRequest, f *pipelineDiffFX) (profileServiceState, profileServiceResult, *pipelineDiffFX) {
	t.Helper()
	if !rustProfileFlowAvailable() {
		t.Fatal("real pipeline DLL required")
	}
	gs, rs := pipelineClone(s), pipelineClone(s)
	gf, rf := pipelineFakeCopy(f), pipelineFakeCopy(f)
	rf.UseRustWorkers = true
	want := runGoProfileService(&gs, q, gf)
	before := profileFlowRecoveries.Load()
	got, handled := runRustProfileService(&rs, q, rf)
	if !handled {
		t.Fatalf("unexpected pipeline fallback op=%s input=%+v reason=%v", q.Op, q, profileFlowRecoveryReason.Load())
	}
	if len(gf.WorkerProblems) > 0 || len(rf.WorkerProblems) > 0 {
		t.Fatalf("worker not actual Rust: %v %v", gf.WorkerProblems, rf.WorkerProblems)
	}
	if !reflect.DeepEqual(gf.WorkerTraces, rf.WorkerTraces) {
		for key, gtrace := range gf.WorkerTraces {
			rtrace := rf.WorkerTraces[key]
			if len(gtrace) != len(rtrace) {
				t.Fatalf("worker trace lengths key=%s Go=%d Rust=%d", key, len(gtrace), len(rtrace))
			}
			for i := range gtrace {
				if !reflect.DeepEqual(gtrace[i].Effect, rtrace[i].Effect) {
					t.Fatalf("worker effect mismatch key=%s step=%d Go=%+v Rust=%+v", key, i, gtrace[i].Effect, rtrace[i].Effect)
				}
				if !reflect.DeepEqual(gtrace[i].State, rtrace[i].State) {
					t.Fatalf("worker state mismatch key=%s step=%d Go=%#v Rust=%#v", key, i, gtrace[i].State, rtrace[i].State)
				}
			}
		}
		t.Fatalf("worker map key mismatch")
	}
	if profileFlowRecoveries.Load() != before {
		t.Fatalf("pipeline required Go continuation reason=%v op=%s input=%+v", profileFlowRecoveryReason.Load(), q.Op, q)
	}
	text := func(e error) string {
		if e == nil {
			return ""
		}
		return e.Error()
	}
	for _, sentinel := range []error{os.ErrNotExist, errNoUsableNodes, errSubscriptionNetwork} {
		if errors.Is(want.Err, sentinel) != errors.Is(got.Err, sentinel) {
			t.Fatalf("error category lost op=%s sentinel=%v", q.Op, sentinel)
		}
	}
	we, ge := text(want.Err), text(got.Err)
	saved := got.Err
	want.Err = nil
	got.Err = nil
	if we != ge || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(gs, rs) || !reflect.DeepEqual(gf.Trace, rf.Trace) || !reflect.DeepEqual(gf.Files, rf.Files) {
		t.Fatalf("pipeline parity op=%s failure=%s/%d/%s\nerr=%q/%q\nresults=%#v/%#v\nstate=%#v/%#v\nfiles=%#v/%#v\ntrace=%#v/%#v", q.Op, f.FailKind, f.FailAt, f.FailPath, we, ge, want, got, gs, rs, gf.Files, rf.Files, gf.Trace, rf.Trace)
	}
	got.Err = saved
	return rs, got, rf
}

func TestMigrationPipelineFlowInstallFaultMatrix(t *testing.T) {
	for _, op := range []string{"cache-id", "cache-active", "use-cache", "cached-sources", "replace", "write-merged", "merge-caches"} {
		for _, kind := range []string{"", "read", "write", "rename", "remove", "mkdir", "parse", "merge-sources", "prepare-live-config", "apply-live-config", "save-settings", "save-nodes"} {
			for nth := 1; nth <= 4; nth++ {
				t.Run(fmt.Sprintf("%s/%s/%d", op, kind, nth), func(t *testing.T) {
					s, f := pipelineDiffBase()
					f.Running = true
					f.FailKind = kind
					f.FailAt = nth
					q := profileServiceRequest{Op: op, ID: "fixture", Path: s.ProfilePath, Body: []byte("new"), Format: "yaml", Sources: []SubscriptionSource{{ID: "fixture", Body: []byte("new")}}}
					pipelineDiffCompare(t, s, q, f)
				})
			}
		}
	}
}
func TestMigrationPipelineFlowSequentialRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(121234))
	pick := func(n int) int { return rng.Intn(n) }
	ops := []string{"cache-id", "cache-active", "use-cache", "cached-sources", "replace", "write-merged", "merge-caches", "proxies", "download", "fetch", "fetch-single", "refresh-single"}
	for i := 0; i < 1600; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, f := pipelineDiffBase()
			f.Running = pick(2) == 0
			f.ProxyOn = pick(2) == 0
			s.MixedPort = []int{0, 7890}[pick(2)]
			s.SelectedChoice = []string{"selected", "missing", autoGroup, blockedChoice}[pick(4)]
			if pick(4) == 0 {
				s.Subscriptions = nil
			}
			if pick(3) == 0 {
				s.Settings.SubscriptionURL = ""
			}
			if pick(3) == 0 {
				s.CachedNodes = nil
			}
			if pick(3) == 0 {
				delete(f.Files, s.ProfilePath)
			}
			if pick(3) == 0 {
				delete(f.Files, s.ProfilePath+".source")
			}
			f.FailKind = []string{"", "read", "write", "rename", "mkdir", "merge-sources", "ensure-yaml", "save-settings", "save-nodes", "apply-live-config", "registry"}[pick(11)]
			f.FailAt = pick(4) + 1
			f.DownloadFails = pick(8)
			f.DownloadNoNodes = pick(2) == 0
			q := profileServiceRequest{Op: ops[pick(len(ops))], ID: []string{"fixture", "missing", ""}[pick(3)], URL: "https://synthetic.invalid/sub?sub=1", Path: s.ProfilePath, Format: "yaml", Body: [][]byte{nil, {}, []byte("new"), []byte("invalid")}[pick(4)], Sources: []SubscriptionSource{{ID: "fixture", Body: []byte("new")}}}
			pipelineDiffCompare(t, s, q, f)
		})
	}
}

// Dedicated assertion verifies compensation, not just parity with the oracle.
func TestMigrationPipelineFlowLiveApplyCompensation(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			s, f := pipelineDiffBase()
			f.Running = true
			f.FailKind = "apply-live-config"
			if !previous {
				delete(f.Files, s.ProfilePath)
			}
			out, res, fx := pipelineDiffCompare(t, s, profileServiceRequest{Op: "write-merged", Sources: []SubscriptionSource{{ID: "fixture", Body: []byte("new")}}}, f)
			if res.Err == nil || !strings.Contains(res.Err.Error(), "synthetic apply-live-config failure") {
				t.Fatal("live failure missing")
			}
			if previous {
				if string(fx.Files[s.ProfilePath]) != "previous" {
					t.Fatal("previous profile not restored")
				}
			} else if _, ok := fx.Files[s.ProfilePath]; ok {
				t.Fatal("new profile retained after failed apply")
			}
			if out.Settings.SubscriptionURL != s.Settings.SubscriptionURL {
				t.Fatal("failed apply committed merged subscription")
			}
		})
	}
}

func TestMigrationPipelineFlowRestoreFailure(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			s, f := pipelineDiffBase()
			f.Running = true
			f.FailKind = "apply-live-config"
			restoreKind := "rename"
			nth := 2
			if !previous {
				delete(f.Files, s.ProfilePath)
				restoreKind = "remove"
				nth = 1
			}
			f.FailExtra = map[string]int{restoreKind: nth}
			_, res, _ := pipelineDiffCompare(t, s, profileServiceRequest{Op: "write-merged", Sources: []SubscriptionSource{{ID: "fixture", Body: []byte("new")}}}, f)
			if res.Err == nil || !strings.Contains(res.Err.Error(), "synthetic apply-live-config failure") || !strings.Contains(res.Err.Error(), "synthetic "+restoreKind+" failure") {
				t.Fatalf("joined compensation failure lost: %v", res.Err)
			}
		})
	}
}
func TestMigrationPipelineFlowActiveNodeProtection(t *testing.T) {
	for _, choice := range []string{"missing", "manual", autoGroup, fallbackGroup, chainExit, blockedChoice, ""} {
		t.Run(choice, func(t *testing.T) {
			s, f := pipelineDiffBase()
			f.Running = true
			s.SelectedChoice = choice
			s.ManualNodes = []manualNode{{Name: "manual"}}
			_, res, fx := pipelineDiffCompare(t, s, profileServiceRequest{Op: "write-merged", Sources: []SubscriptionSource{{ID: "fixture", Body: []byte("new")}}}, f)
			if choice == "missing" {
				if res.Err == nil || fx.Calls["write"] != 0 || fx.Calls["apply-live-config"] != 0 || string(fx.Files[s.ProfilePath]) != "previous" {
					t.Fatal("active missing node protection happened after destructive effects")
				}
			} else if res.Err != nil {
				t.Fatalf("allowed active/manual/group choice rejected: %v", res.Err)
			}
		})
	}
}

func TestMigrationPipelineFlowDownloadAttemptOrder(t *testing.T) {
	for _, running := range []bool{false, true} {
		for failed := 0; failed <= 7; failed++ {
			t.Run(fmt.Sprintf("running=%v/fail=%d", running, failed), func(t *testing.T) {
				s, f := pipelineDiffBase()
				f.Running = running
				f.ProxyOn = true
				f.DownloadFails = failed
				f.DownloadNoNodes = failed%2 == 0
				url := "https://synthetic.invalid/api?sub=1"
				alt, _ := clashFormatURL(url)
				_, res, fx := pipelineDiffCompare(t, s, profileServiceRequest{Op: "download", URL: url}, f)
				proxies := []string{"direct", f.Proxy, ""}
				if running {
					proxies = []string{"127.0.0.1:7890", "direct"}
				}
				var attempts []profileEffect
				for _, e := range fx.Trace {
					if e.Effect.Kind == "download" {
						attempts = append(attempts, e.Effect)
					}
				}
				wantCount := min(failed+1, len(proxies)*2)
				if len(attempts) != wantCount {
					t.Fatalf("attempt count=%d want%d", len(attempts), wantCount)
				}
				for i, a := range attempts {
					wantURL := url
					if i%2 != 0 {
						wantURL = alt
					}
					if a.URL != wantURL || a.Proxy != proxies[i/2] {
						t.Fatalf("attempt%d violated proxy-outer URL-inner priority: %+v", i, a)
					}
				}
				if failed >= len(proxies)*2 {
					if res.Err == nil {
						t.Fatal("allfailed download unexpectedly succeeded")
					}
				} else if res.Err != nil {
					t.Fatalf("available attempt not used: %v", res.Err)
				}
			})
		}
	}
}
func TestMigrationPipelineFlowAutoTickBoundary(t *testing.T) {
	for _, hours := range []int{0, -1, 1, 720} {
		for _, delta := range []int64{-1, 0, 1} {
			t.Run(fmt.Sprintf("%d/%d", hours, delta), func(t *testing.T) {
				s, f := pipelineDiffBase()
				f.Now = 1800000000
				s.Settings.AutoMergeHours = hours
				s.Subscriptions[0].UpdatedAt = f.Now - int64(hours)*3600 + delta
				_, _, fx := pipelineDiffCompare(t, s, profileServiceRequest{Op: "auto-tick"}, f)
				due := hours > 0 && delta <= 0
				if !due && len(fx.WorkerTraces) != 0 {
					t.Fatal("auto tick downloaded before due or while disabled")
				}
				if due && len(fx.WorkerTraces) == 0 {
					t.Fatal("auto tick exact deadline skipped")
				}
			})
		}
	}
}

func TestMigrationPipelineFlowErrorIdentity(t *testing.T) {
	for mode := 0; mode <= 4; mode++ {
		for _, op := range []string{"download", "fetch"} {
			t.Run(fmt.Sprintf("%s/%d", op, mode), func(t *testing.T) {
				s, f := pipelineDiffBase()
				f.Files = map[string][]byte{}
				s.CachedNodes = nil
				f.DownloadFails = 99
				f.DownloadNoNodes = true
				f.DownloadMode = mode
				_, res, _ := pipelineDiffCompare(t, s, profileServiceRequest{Op: op, URL: s.Settings.SubscriptionURL}, f)
				if op == "fetch" {
					wantNoNodes := mode == 0 || mode == 1
					if errors.Is(res.Err, errNoUsableNodes) != wantNoNodes || errors.Is(res.Err, errSubscriptionNetwork) == wantNoNodes {
						t.Fatalf("wrapped vs same-text error classification mode%d: %v", mode, res.Err)
					}
				}
			})
		}
	}
}

// The batch uses the production goroutine barrier, but each worker executes only
// fake ports. Worker traces are compared per entry; their scheduling order is
// deliberately not required to be identical. All post-barrier effects stay exact.
func (f *pipelineDiffFX) batch(q profileEffect, s *profileServiceState) (reply profileReply) {
	f.mu.Lock()
	f.Trace = append(f.Trace, pipelineDiffEvent{settingsDiffCopy(reflect.ValueOf(q)).Interface().(profileEffect), pipelineClone(*s)})
	f.WorkerTraces = map[string][]pipelineDiffEvent{}
	f.mu.Unlock()
	gate := make(chan struct{})
	var once sync.Once
	reply.Results = profileAcquireBatch(q.Entries, func(entry subscriptionEntry) profileAcquisitionResult {
		if f.ReverseWorkers && len(q.Entries) > 1 && entry.ID == q.Entries[0].ID {
			<-gate
		}
		child := pipelineFakeCopy(f)
		child.ReverseWorkers = false
		cs := pipelineClone(*s)
		req := profileServiceRequest{Op: "acquire-entry", Entry: entry}
		var result profileServiceResult
		if f.UseRustWorkers {
			var handled bool
			result, handled = runRustProfileService(&cs, req, child)
			if !handled {
				f.mu.Lock()
				f.WorkerProblems = append(f.WorkerProblems, "unexpected fallback "+entry.ID)
				f.mu.Unlock()
			}
		} else {
			result = runGoProfileService(&cs, req, child)
		}
		f.mu.Lock()
		f.WorkerTraces[entry.ID+"|"+entry.URL] = child.Trace
		f.Completion = append(f.Completion, entry.ID)
		f.mu.Unlock()
		if f.ReverseWorkers && len(q.Entries) > 1 && entry.ID == q.Entries[len(q.Entries)-1].ID {
			once.Do(func() { close(gate) })
		}
		if result.Err != nil {
			f.mu.Lock()
			f.WorkerProblems = append(f.WorkerProblems, fmt.Sprintf("worker %s result status=%d error=%v", entry.ID, result.Status, result.Err))
			f.mu.Unlock()
			result.Acquisition.ID = entry.ID
			result.Acquisition.Err = result.Err
		}
		return result.Acquisition
	})
	return
}
func TestMigrationPipelineFlowConcurrentAcquisitionOrder(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			s, f := pipelineDiffBase()
			f.ReverseWorkers = true
			s.Subscriptions = []subscriptionEntry{{ID: "first", URL: "https://first.invalid/sub", Label: "first", Enabled: true}, {ID: "last", URL: "https://last.invalid/sub", Label: "last", Enabled: true}}
			if failure {
				f.DownloadFails = 99
				for _, id := range []string{"first", "last"} {
					f.Files[filepath.Join(s.Home, "subscriptions", id+".yaml")] = []byte("cached")
				}
			}
			_, res, fx := pipelineDiffCompare(t, s, profileServiceRequest{Op: "merge-all"}, f)
			if res.Err != nil {
				t.Fatal(res.Err)
			}
			if !reflect.DeepEqual(fx.Completion, []string{"last", "first"}) {
				t.Fatalf("test did not force reversed worker completion: %v", fx.Completion)
			}
			saw := false
			for _, e := range fx.Trace {
				if e.Effect.Kind == "merge-sources" {
					saw = true
					if len(e.Effect.Sources) != 2 || e.Effect.Sources[0].ID != "first" || e.Effect.Sources[1].ID != "last" {
						t.Fatal("merge source order followed completion order")
					}
				}
			}
			if !saw {
				t.Fatal("missing final merge-sources computation")
			}
		})
	}
}

func TestMigrationPipelineFlowCacheFallbackLayers(t *testing.T) {
	for _, layer := range []string{"matching-live", "id-cache", "id-cache-install-fails", "loose-live", "memory", "none"} {
		t.Run(layer, func(t *testing.T) {
			s, f := pipelineDiffBase()
			f.DownloadFails = 99
			f.DownloadNoNodes = true
			cache := filepath.Join(s.Home, "subscriptions", "fixture.yaml")
			if layer != "matching-live" {
				f.Files[s.ProfilePath+".source"] = []byte("wrong-owner")
			}
			if layer == "id-cache" || layer == "id-cache-install-fails" {
				f.Files[s.ProfilePath] = []byte("invalid")
			}
			if layer == "id-cache-install-fails" {
				f.FailKind = "write"
				f.FailAt = 1
			}
			if layer == "loose-live" {
				delete(f.Files, cache)
			}
			if layer == "memory" || layer == "none" {
				f.Files = map[string][]byte{}
			}
			if layer == "none" {
				s.CachedNodes = nil
			}
			out, res, _ := pipelineDiffCompare(t, s, profileServiceRequest{Op: "fetch"}, f)
			if layer == "none" {
				if !errors.Is(res.Err, errNoUsableNodes) {
					t.Fatalf("missing no-node failure: %v", res.Err)
				}
				return
			}
			if res.Err != nil || !out.ProfileFromCache {
				t.Fatalf("cache layer rejected: err=%v cached=%v", res.Err, out.ProfileFromCache)
			}
			if layer == "memory" {
				if !reflect.DeepEqual(res.Nodes, s.CachedNodes) {
					t.Fatal("memory fallback changed node list")
				}
			}
			if layer == "id-cache-install-fails" {
				if !reflect.DeepEqual(out.CachedNodes, s.CachedNodes) || len(res.Nodes) != 2 {
					t.Fatal("ignored install failure must preserve old memory yet return parsed cached nodes")
				}
			}
			if layer == "matching-live" && res.Nodes[0].SourceLabel != "fixture-label" {
				t.Fatal("matching live profile lacks source label")
			}
			if layer == "loose-live" && res.Nodes[0].SourceLabel != "" {
				t.Fatal("legacy loose-live fallback unexpectedly relabeled")
			}
		})
	}
}

func TestMigrationPipelineFlowAcquisitionCacheWarning(t *testing.T) {
	s, f := pipelineDiffBase()
	f.DownloadFails = 99
	_, result, _ := pipelineDiffCompare(t, s, profileServiceRequest{Op: "acquire-entry", Entry: s.Subscriptions[0]}, f)
	if result.Err != nil || result.Acquisition.Err == nil || len(result.Acquisition.Nodes) != 2 {
		t.Fatal("cached acquisition should carry nodes and a warning, not fail the whole worker")
	}
}

func TestMigrationPipelineFixtureCloneIsolation(t *testing.T) {
	s, _ := pipelineDiffBase()
	s.ManualNodes = []manualNode{{Name: "manual", Proxy: map[string]any{"nested": []string{"original"}}}}
	c := pipelineClone(s)
	c.Subscriptions[0].Label = "changed"
	c.Settings.Chains["untouched"][0] = "changed"
	c.Health["old-node"] = nodeHealth{Name: "changed"}
	c.Regions["old-node"].Sources[0] = "changed"
	c.ManualNodes[0].Proxy["nested"].([]string)[0] = "changed"
	if s.Subscriptions[0].Label != "fixture-label" || s.Settings.Chains["untouched"][0] != "a" || s.Health["old-node"].Name != "old-node" || s.Regions["old-node"].Sources[0] != "first" || s.ManualNodes[0].Proxy["nested"].([]string)[0] != "original" {
		t.Fatal("differential fixture graphs alias through embedded state")
	}
}
