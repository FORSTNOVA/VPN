//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

type subDiffEvent struct {
	Kind, Arg string
	State     subscriptionServiceState
}
type subDiffFX struct {
	Trace                                  []subDiffEvent
	Fail                                   map[string]bool
	Loaded                                 []subscriptionLoadedEntry
	Running, Matches, Partial, FetchCached bool
	Now                                    int64
	EmptySecret                            bool
}

func (f *subDiffFX) Call(kind, arg string, s *subscriptionServiceState) (r subscriptionEffectReply) {
	f.Trace = append(f.Trace, subDiffEvent{kind, arg, settingsDiffClone(*s)})
	if f.Fail[kind] {
		r.Err = errors.New("synthetic " + kind + " failure")
	}
	switch kind {
	case "read-registry":
		r.Loaded = settingsDiffCopy(reflect.ValueOf(f.Loaded)).Interface().([]subscriptionLoadedEntry)
	case "open-secret":
		r.Text = strings.TrimPrefix(arg, "sealed:")
		if f.EmptySecret {
			r.Text = ""
		}
	case "seal-secret":
		r.Text = "sealed:" + arg
		if f.EmptySecret {
			r.Text = ""
		}
	case "describe-url":
		r.Entry = subscriptionEntry{ID: subscriptionHash(arg), Label: subscriptionLabel(arg)}
	case "now":
		r.Timestamp = f.Now
		f.Now++
	case "running":
		r.Bool = f.Running
	case "profile-matches":
		r.Bool = f.Matches
	case "fetch", "use-cache", "merge", "merge-caches":
		if r.Err == nil || f.Partial {
			s.CachedNodes = []proxyNode{{Name: "synthetic-" + kind}, {Name: "second"}}
			s.ProfileFromCache = f.FetchCached
			s.Settings.SelectedNode = "effect-" + kind
			if len(s.Subscriptions) > 0 {
				s.Subscriptions[0].QuotaError = "effect-" + kind
			}
		}
		r.Count = 3
	case "write-registry", "start-loop", "log", "save-settings", "remove-nodes", "clear-health", "clear-regions", "remove-config", "remove-profile", "remove-cache", "remove-cache-format":
	default:
		panic("unexpected fake effect " + kind)
	}
	return
}
func subDiffCompare(t *testing.T, initial subscriptionServiceState, req subscriptionServiceRequest, f subDiffFX) (subscriptionServiceState, subscriptionServiceResult, []subDiffEvent) {
	t.Helper()
	if !rustSubscriptionFlowAvailable() {
		t.Fatal("real subscription-flow DLL required")
	}
	g, r := settingsDiffClone(initial), settingsDiffClone(initial)
	gf, rf := f, f
	req = prepareSubscriptionServiceRequest(req)
	want := runGoSubscriptionService(&g, req, &gf)
	recoveriesBefore := subscriptionFlowRecoveries.Load()
	got, handled := runRustSubscriptionService(&r, req, &rf)
	activeCount := 0
	for _, e := range initial.Subscriptions {
		if e.URL == initial.Settings.SubscriptionURL {
			activeCount++
		}
	}
	expectedFallback := req.Op == "list" && activeCount > 1
	if expectedFallback {
		if handled || len(rf.Trace) != 0 || !reflect.DeepEqual(r, initial) {
			t.Fatal("duplicate-active fallback must occur before effects/state changes")
		}
		t.Setenv("SMARTVPN_RUST_SUBSCRIPTION_FLOW", "1")
		got = runSelectedSubscriptionService(&r, req, &rf)
		t.Log("verified explicit duplicate-active list Go fallback; not counted as actual Rust execution")
	} else if !handled {
		t.Fatal("unexpected fallback")
	}
	if subscriptionFlowRecoveries.Load() != recoveriesBefore {
		t.Fatalf("Rust plan/ABI mismatch required Go continuation; parity is not a Rust pass: reason=%v op=%s request=%+v effects=%+v", subscriptionFlowRecoveryReason.Load(), req.Op, req, rf.Trace)
	}
	errorText := func(e error) string {
		if e == nil {
			return ""
		}
		return e.Error()
	}
	originalError := got.Err
	we, ge := errorText(want.Err), errorText(got.Err)
	want.Err = nil
	got.Err = nil
	if we != ge || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(g, r) || !reflect.DeepEqual(gf.Trace, rf.Trace) {
		t.Fatalf("subscription parity op=%s req=%+v\nresult Go=%#v Rust=%#v err=%q/%q\nstateGo=%#v\nstateRust=%#v\ntraceGo=%#v\ntraceRust=%#v", req.Op, req, want, got, we, ge, g, r, gf.Trace, rf.Trace)
	}
	got.Err = originalError
	return r, got, rf.Trace
}
func subDiffBase() subscriptionServiceState {
	s := settingsDiffBase()
	s.Subscriptions = []subscriptionEntry{
		{ID: "dup", URL: "https://old.example/sub", Label: "old", Enabled: true, NodeCount: 8, UpdatedAt: 7, LastError: "old-error", UserInfo: &subscriptionUserInfo{Upload: 1, Download: 2, Total: 9007199254740993, Expire: 4}},
		{ID: "dup", URL: "https://second.example/sub", Label: "second", Enabled: true},
		{ID: "third", URL: "https://old.example/sub", Label: "old", Enabled: false},
	}
	return s
}
func TestMigrationSubscriptionFlowRandomParity(t *testing.T) {
	rng := rand.New(rand.NewSource(111234))
	pick := func(n int) int { return rng.Intn(n) }
	ops := []string{"load", "save", "ensure", "find", "find-url", "list", "add", "update", "rename", "record-use", "record-error", "hours", "delete", "activate"}
	urls := []string{"", "https://old.example/sub", "https://new.example/sub", mergedSubscriptionURL, "ftp://invalid", " https://second.example/sub "}
	for i := 0; i < 2400; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := subDiffBase()
			s.StoreAvailable = pick(2) == 0
			s.Settings.SubscriptionURL = urls[pick(4)]
			if pick(4) == 0 {
				s.Subscriptions = nil
			}
			if pick(4) == 0 {
				s.Subscriptions = []subscriptionEntry{}
			}
			req := subscriptionServiceRequest{Op: ops[pick(len(ops))], ID: []string{"dup", "third", "missing", " dup ", "", mergedSubscriptionURL}[pick(6)], URL: urls[pick(len(urls))], Label: []string{"", " x ", "节点🧪", strings.Repeat("x", 41)}[pick(4)], Prefix: " p ", HasPrefix: pick(2) == 0, HasEnabled: pick(2) == 0, Enabled: pick(2) == 0, Hours: []int{-1, 0, 1, 720, 721}[pick(5)], NodeCount: pick(10), Failure: "reported failure"}
			if pick(2) == 0 {
				req.Info = &subscriptionUserInfo{Upload: 9007199254740993, Download: 2, Total: 3, Expire: 4}
			}
			f := subDiffFX{Running: pick(5) == 0, Matches: pick(2) == 0, Partial: pick(2) == 0, FetchCached: pick(2) == 0, Now: 1800000000, Fail: map[string]bool{}}
			for _, kind := range []string{"open-secret", "seal-secret", "write-registry", "save-settings", "clear-health", "clear-regions", "fetch", "use-cache", "merge", "merge-caches"} {
				f.Fail[kind] = pick(4) == 0
			}
			f.Loaded = []subscriptionLoadedEntry{{Entry: subscriptionEntry{URL: "sealed:https://legacy.example/sub"}}, {Entry: subscriptionEntry{URL: "sealed:https://false.example/sub", Enabled: false}, HasEnabled: true}, {Entry: subscriptionEntry{URL: ""}}}
			subDiffCompare(t, s, req, f)
		})
	}
}

func TestMigrationSubscriptionFlowListDuplicateParity(t *testing.T) {
	for _, n := range []int{0, 1, 2, 12, 21, 50, 101} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := subDiffBase()
			s.Subscriptions = nil
			for i := 0; i < n; i++ {
				url := "https://other.example/sub"
				if i%3 != 0 {
					url = s.Settings.SubscriptionURL
				}
				s.Subscriptions = append(s.Subscriptions, subscriptionEntry{ID: fmt.Sprint(i), URL: url, Label: []string{"same", "z", "a"}[i%3]})
			}
			subDiffCompare(t, s, subscriptionServiceRequest{Op: "list"}, subDiffFX{})
		})
	}
}

func TestMigrationSubscriptionFlowMutationFailureContracts(t *testing.T) {
	for _, op := range []string{"add", "update", "rename", "delete", "hours"} {
		t.Run(op, func(t *testing.T) {
			s := subDiffBase()
			before := settingsDiffClone(s)
			r := subscriptionServiceRequest{Op: op, ID: "dup", URL: "https://new.example/sub", Label: "new", Hours: 25, HasEnabled: true, Enabled: false, HasPrefix: true}
			out, res, tr := subDiffCompare(t, s, r, subDiffFX{Fail: map[string]bool{"write-registry": true, "save-settings": true}})
			if res.Status != 500 {
				t.Fatalf("expected save500 %+v", res)
			}
			switch op {
			case "add":
				if !reflect.DeepEqual(before.Subscriptions, out.Subscriptions) {
					t.Fatal("add failure did not retract tail")
				}
			case "update":
				if out.Subscriptions[0].Label != "new" || out.Subscriptions[1].Label != "second" {
					t.Fatal("update must keep first-ID partial mutation only")
				}
			case "rename":
				if out.Subscriptions[0].Label != "new" || out.Subscriptions[1].Label != "new" {
					t.Fatal("rename must retain all-ID mutation")
				}
			case "hours":
				if out.Settings.AutoMergeHours != 25 {
					t.Fatal("hours save error rolled back memory")
				}
			case "delete":
				if !reflect.DeepEqual(before.Subscriptions, out.Subscriptions) || out.Settings.SubscriptionURL != "" || len(out.Health) != 0 || out.LockedNode != "" {
					t.Fatal("delete must restore registry only, not cleared active state")
				}
				writes := 0
				for _, e := range tr {
					if e.Kind == "write-registry" {
						writes++
					}
					if e.Kind == "remove-cache" || e.Kind == "remove-cache-format" {
						t.Fatal("failed delete removed cache")
					}
				}
				if writes != 2 {
					t.Fatalf("delete rollback save count %d", writes)
				}
			}
		})
	}
}
func TestMigrationSubscriptionFlowActivateMatrix(t *testing.T) {
	for i := 0; i < 64; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := subDiffBase()
			if i&1 != 0 {
				s.Settings.SubscriptionURL = "different"
			}
			f := subDiffFX{Running: i&2 != 0, Matches: i&4 != 0, Partial: true, FetchCached: i&8 != 0, Now: 1800000000, Fail: map[string]bool{"fetch": i&16 != 0, "use-cache": i&32 != 0}}
			out, res, trace := subDiffCompare(t, s, subscriptionServiceRequest{Op: "activate", ID: "dup"}, f)
			counts := map[string]int{}
			for _, e := range trace {
				counts[e.Kind]++
			}
			if f.Running {
				if res.Status != 409 || len(trace) != 1 {
					t.Fatal("running must reject before lookup/effects")
				}
				return
			}
			if s.Settings.SubscriptionURL == s.Subscriptions[0].URL && f.Matches {
				if res.Status != 200 || counts["fetch"] != 0 || counts["remove-nodes"] != 0 {
					t.Fatal("active matching profile failed short circuit")
				}
				return
			}
			if counts["fetch"] != 1 || counts["remove-nodes"] != 1 {
				t.Fatal("activation fetch/clear count")
			}
			if f.Fail["fetch"] && f.Fail["use-cache"] {
				if res.Status != 502 || res.Message != "synthetic fetch failure" || out.Settings.SubscriptionURL != s.Subscriptions[0].URL || out.Subscriptions[0].LastError != "synthetic fetch failure" || out.Subscriptions[1].LastError != "synthetic fetch failure" {
					t.Fatal("double failure must preserve partial state and original fetch error on all IDs")
				}
			} else if res.Status != 200 {
				t.Fatal("successful fetch/cache activation failed")
			}
		})
	}
}
func TestMigrationSubscriptionFlowLegacyEnabled(t *testing.T) {
	s := subDiffBase()
	s.Settings.SubscriptionURL = ""
	loaded := []subscriptionLoadedEntry{
		{Entry: subscriptionEntry{URL: "sealed:https://missing.example/sub"}},
		{Entry: subscriptionEntry{URL: "sealed:https://false.example/sub"}, HasEnabled: true},
		{Entry: subscriptionEntry{URL: "sealed:https://null.example/sub"}, HasEnabled: true},
		{Entry: subscriptionEntry{URL: "sealed:https://true.example/sub", Enabled: true}, HasEnabled: true},
		{Entry: subscriptionEntry{URL: ""}},
	}
	out, _, tr := subDiffCompare(t, s, subscriptionServiceRequest{Op: "load"}, subDiffFX{Loaded: loaded})
	if len(out.Subscriptions) != 4 {
		t.Fatal("empty stored URL should be dropped")
	}
	for i, want := range []bool{true, false, false, true} {
		if out.Subscriptions[i].Enabled != want {
			t.Fatalf("legacy entry%d enabled=%v", i, out.Subscriptions[i].Enabled)
		}
	}
	if tr[len(tr)-1].Kind != "start-loop" {
		t.Fatal("load must finish with start-loop effect")
	}
}

func TestMigrationSubscriptionFlowDeleteMergedPartial(t *testing.T) {
	s := subDiffBase()
	s.Settings.SubscriptionURL = mergedSubscriptionURL
	s.Subscriptions[2].Enabled = true
	before := settingsDiffClone(s)
	out, res, tr := subDiffCompare(t, s, subscriptionServiceRequest{Op: "delete", ID: "dup"}, subDiffFX{Partial: true, Fail: map[string]bool{"merge-caches": true}})
	if res.Status != 502 || !reflect.DeepEqual(out.Subscriptions, before.Subscriptions) || out.Settings.SelectedNode != "effect-merge-caches" || len(out.CachedNodes) != 2 {
		t.Fatal("merged delete rollback must restore list but preserve composite partial state")
	}
	writes := 0
	for _, e := range tr {
		if e.Kind == "write-registry" {
			writes++
		}
		if e.Kind == "remove-cache" {
			t.Fatal("failed merge deleted cache")
		}
	}
	if writes != 1 {
		t.Fatalf("rollback not persisted once: %d", writes)
	}
}

func TestMigrationSubscriptionFlowSequenceBytes(t *testing.T) {
	for _, name := range []string{"simple", "节点🧪", "raw\xff\x00"} {
		t.Run(fmt.Sprintf("%x", name), func(t *testing.T) {
			s := subDiffBase()
			for i := range s.Subscriptions {
				s.Subscriptions[i].Label = name
				s.Subscriptions[i].Prefix = name
			}
			reqs := []subscriptionServiceRequest{
				{Op: "find", ID: "dup"}, {Op: "list"}, {Op: "rename", ID: "dup", Label: name},
				{Op: "update", ID: " dup ", URL: "ftp://invalid", HasPrefix: true, Prefix: name, HasEnabled: true},
				{Op: "record-use", ID: "dup", NodeCount: 7, Failure: name, Info: &subscriptionUserInfo{Upload: 9223372036854775807, Download: -9223372036854775808, Total: 9007199254740993}},
				{Op: "record-error", ID: "dup", Failure: name}, {Op: "save"}, {Op: "ensure", URL: "https://new.example/sub"},
				{Op: "find-url", URL: "https://new.example/sub"}, {Op: "hours", Hours: 720}, {Op: "delete", ID: "third"}, {Op: "list"},
			}
			for _, req := range reqs {
				s, _, _ = subDiffCompare(t, s, req, subDiffFX{Now: 1800000000})
			}
		})
	}
}

func TestMigrationSubscriptionFlowEmptySecretSuccess(t *testing.T) {
	s := subDiffBase()
	s.Settings.SubscriptionURL = ""
	out, _, _ := subDiffCompare(t, s, subscriptionServiceRequest{Op: "load"}, subDiffFX{EmptySecret: true, Loaded: []subscriptionLoadedEntry{{Entry: subscriptionEntry{URL: "nonempty-sealed-input"}}}})
	if len(out.Subscriptions) != 1 || out.Subscriptions[0].URL != "" {
		t.Fatal("successful open to empty plaintext must not drop entry")
	}
	_, res, trace := subDiffCompare(t, subDiffBase(), subscriptionServiceRequest{Op: "save"}, subDiffFX{EmptySecret: true})
	if res.Err != nil {
		t.Fatal("empty successful seal unexpectedly failed")
	}
	found := false
	for _, e := range trace {
		if e.Kind == "write-registry" {
			found = true
			for _, entry := range e.State.Subscriptions {
				if entry.URL != "" {
					t.Fatal("empty successful seal not written")
				}
			}
		}
	}
	if !found {
		t.Fatal("empty seals skipped write")
	}
}

func TestMigrationSubscriptionFlowWideTimestamp(t *testing.T) {
	for _, now := range []int64{2147483648, 9007199254740993, 9223372036854775700} {
		t.Run(fmt.Sprint(now), func(t *testing.T) {
			out, _, _ := subDiffCompare(t, subDiffBase(), subscriptionServiceRequest{Op: "record-use", ID: "dup", NodeCount: 1, Info: &subscriptionUserInfo{Total: 1}}, subDiffFX{Now: now})
			if out.Subscriptions[0].UpdatedAt != now || out.Subscriptions[0].QuotaUpdatedAt != now+1 || out.Subscriptions[1].UpdatedAt != now+2 || out.Subscriptions[1].QuotaUpdatedAt != now+3 {
				t.Fatal("timestamp truncation or missing per-entry clock effects")
			}
		})
	}
}

func TestMigrationSubscriptionFlowActivateIgnoresUnrelatedFields(t *testing.T) {
	s := subDiffBase()
	out, res, tr := subDiffCompare(t, s, subscriptionServiceRequest{Op: "activate", ID: "dup", Info: &subscriptionUserInfo{Upload: 999}, Failure: "unrelated", NodeCount: 999}, subDiffFX{Now: 1800000000})
	if res.Status != 200 || out.Subscriptions[0].UserInfo.Upload != 1 || out.Subscriptions[1].UserInfo != nil {
		t.Fatal("activation imported unrelated quota fields")
	}
	nowCalls := 0
	for _, e := range tr {
		if e.Kind == "now" {
			nowCalls++
		}
	}
	if nowCalls != 2 {
		t.Fatalf("activation clock effects=%d want one per matching ID", nowCalls)
	}
}
