package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type profileMemoryEffects struct {
	Trace     []profileEffect
	Files     map[string][]byte
	Fail      map[string]error
	Nodes     []proxyNode
	Running   bool
	Downloads []profileReply
	Applied   int
}

func (f *profileMemoryEffects) Call(q profileEffect, s *profileServiceState) (r profileReply) {
	f.Trace = append(f.Trace, q)
	switch q.Kind {
	case "read":
		var found bool
		r.Body, found = f.Files[q.Path]
		if !found {
			r.Err = os.ErrNotExist
		}
	case "write":
		f.Files[q.Path] = append([]byte(nil), q.Body...)
	case "rename":
		f.Files[q.OtherPath] = f.Files[q.Path]
		delete(f.Files, q.Path)
	case "remove":
		delete(f.Files, q.Path)
	case "parse", "ensure-yaml", "merge-sources", "merge-profiles":
		r.Body = []byte("merged")
		r.Nodes = append([]proxyNode(nil), f.Nodes...)
	case "running":
		r.Bool = f.Running
	case "prepare-live-config":
		r.Text = "synthetic-config"
	case "apply-live-config":
		f.Applied++
	case "download":
		if len(f.Downloads) > 0 {
			r = f.Downloads[0]
			f.Downloads = f.Downloads[1:]
		} else {
			r.Err = errSubscriptionNetwork
		}
	}
	if err := f.Fail[q.Kind]; err != nil {
		r.Err = err
	}
	return
}
func profileTestState() profileServiceState {
	return profileServiceState{settingsServiceState: settingsServiceState{Settings: settings{SubscriptionURL: "https://test.invalid/sub", SelectedNode: "one"}, Subscriptions: []subscriptionEntry{{ID: "id", URL: "https://test.invalid/sub", Label: "label", Enabled: true}}}, Home: "synthetic", ProfilePath: "synthetic/live.yaml", SelectedChoice: autoGroup}
}
func TestProfileServiceCachePartial(t *testing.T) {
	s := profileTestState()
	a := profileService{s: &s}
	path := a.profileCachePath("id")
	f := &profileMemoryEffects{Files: map[string][]byte{path: []byte("yaml")}, Nodes: []proxyNode{{Name: "one"}}, Fail: map[string]error{"save-nodes": errors.New("save failed")}}
	r := runGoProfileService(&s, profileServiceRequest{Op: "use-cache", ID: "id"}, f)
	if r.Err == nil || len(s.CachedNodes) != 1 || !s.CachedNodes[0].Selected || s.CachedNodes[0].SourceLabel != "label" {
		t.Fatal(r, s.CachedNodes)
	}
	if string(f.Files[s.ProfilePath+".source"]) != subscriptionHash(s.Settings.SubscriptionURL) {
		t.Fatal(f.Files)
	}
}
func TestProfileServiceMergedCompensation(t *testing.T) {
	s := profileTestState()
	failure := errors.New("apply rejected")
	restore := errors.New("remove denied")
	f := &profileMemoryEffects{Files: map[string][]byte{}, Nodes: []proxyNode{{Name: "one"}}, Running: true, Fail: map[string]error{"apply-live-config": failure, "remove": restore}}
	r := runGoProfileService(&s, profileServiceRequest{Op: "write-merged"}, f)
	if !errors.Is(r.Err, failure) || !errors.Is(r.Err, restore) || f.Applied != 1 || s.Settings.SubscriptionURL == mergedSubscriptionURL {
		t.Fatal(r, f.Applied, s.Settings)
	}
	kinds := []string{}
	for _, q := range f.Trace {
		kinds = append(kinds, q.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"merge-sources", "mkdir", "running", "read", "prepare-live-config", "write", "rename", "apply-live-config", "remove"}) {
		t.Fatal(kinds)
	}
}
func TestProfileServiceFetchCacheQuirk(t *testing.T) {
	s := profileTestState()
	f := &profileMemoryEffects{Files: map[string][]byte{s.ProfilePath: []byte("invalid"), s.ProfilePath + ".source": []byte(subscriptionHash(s.Settings.SubscriptionURL))}}
	r := runGoProfileService(&s, profileServiceRequest{Op: "fetch"}, f)
	// parse-empty does not match profile, so this case must reach network error.
	if !errors.Is(r.Err, errSubscriptionNetwork) {
		t.Fatal(r)
	}
	f = &profileMemoryEffects{Files: map[string][]byte{}, Downloads: []profileReply{{Err: errors.New("first")}, {Err: errors.New("second")}}}
	r = runGoProfileService(&s, profileServiceRequest{Op: "download", URL: "https://test.invalid/sub?clash=1"}, f)
	if r.Err == nil || len(f.Trace) == 0 {
		t.Fatal(r)
	}
}
func TestProfileServiceConcurrentBatchOrdered(t *testing.T) {
	start := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan []profileAcquisitionResult, 1)
	go func() {
		done <- profileAcquireBatch([]subscriptionEntry{{ID: "first"}, {ID: "second"}}, func(e subscriptionEntry) profileAcquisitionResult {
			start <- e.ID
			<-release
			return profileAcquisitionResult{ID: e.ID}
		})
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-start:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("workers were serialized")
		}
	}
	close(release)
	r := <-done
	if len(r) != 2 || r[0].ID != "first" || r[1].ID != "second" {
		t.Fatal(r)
	}
}
func TestProfileServiceProxyOrder(t *testing.T) {
	s := profileTestState()
	s.MixedPort = 1234
	f := &profileMemoryEffects{Running: true}
	r := runGoProfileService(&s, profileServiceRequest{Op: "proxies"}, f)
	if strings.Join(r.Proxies, "|") != "127.0.0.1:1234|direct" {
		t.Fatal(r.Proxies)
	}
}
