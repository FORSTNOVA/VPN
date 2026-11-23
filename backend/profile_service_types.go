package main

import (
	"errors"
	"log"
	"os"
	"sync"
	"time"
)

type profileServiceState struct {
	settingsServiceState
	Home, ProfilePath, SelectedChoice string
	MixedPort                         int
	ManualNodes                       []manualNode
}
type profileServiceRequest struct {
	Entry                     subscriptionEntry
	Op, ID, URL, Path, Format string
	Body                      []byte
	Sources                   []SubscriptionSource
}
type profileServiceResult struct {
	Acquisition profileAcquisitionResult
	Err         error
	Body        []byte
	Nodes       []proxyNode
	Info        *subscriptionUserInfo
	Entry       *subscriptionEntry
	Sources     []SubscriptionSource
	Proxies     []string
	Data        map[string]any
	Status      int
}
type profileEffect struct {
	Entries                                                []subscriptionEntry
	Kind, Path, OtherPath, URL, Proxy, ID, Format, Failure string
	Body                                                   []byte
	Parts                                                  [][]byte
	Sources                                                []SubscriptionSource
	Nodes                                                  []proxyNode
	Count                                                  int
	Info                                                   *subscriptionUserInfo
	Mode                                                   uint32
	Registry                                               subscriptionServiceRequest
}
type profileReply struct {
	Results   []profileAcquisitionResult
	Err       error
	Body      []byte
	Nodes     []proxyNode
	Info      *subscriptionUserInfo
	Text      string
	Bool      bool
	Timestamp int64
	Entry     subscriptionEntry
	Found     bool
}
type profileServiceEffects interface {
	Call(profileEffect, *profileServiceState) profileReply
}

func profileStateFromApp(a *app) profileServiceState {
	return profileServiceState{settingsServiceState: settingsStateFromApp(a), Home: a.home, ProfilePath: a.profilePath(), SelectedChoice: a.selectedChoice(), MixedPort: a.mixedPort, ManualNodes: a.manualSnapshotLocked()}
}
func (a *app) runProfileService(req profileServiceRequest) (out profileServiceResult) {
	defer func() {
		if recover() != nil {
			out = profileServiceResult{Status: 500, Err: errors.New("profile operation interrupted")}
		}
	}()
	s := profileStateFromApp(a)
	defer func() { applySettingsServiceState(a, s.settingsServiceState) }()
	return runSelectedProfileService(&s, req, appProfileEffects{a})
}

type appProfileEffects struct{ a *app }

func (e appProfileEffects) Call(q profileEffect, s *profileServiceState) (r profileReply) {
	// Read/compute/download ports are safe for concurrent acquisition workers.
	// Only explicit serial mutation ports synchronize the modeled application state.
	switch q.Kind {
	case "acquire-batch":
		r.Results = profileAcquireBatch(q.Entries, func(en subscriptionEntry) profileAcquisitionResult {
			child := *s
			out := runSelectedProfileService(&child, profileServiceRequest{Op: "acquire-entry", Entry: en}, e)
			if out.Err != nil {
				out.Acquisition.ID = en.ID
				out.Acquisition.Err = out.Err
			}
			return out.Acquisition
		})
	case "read":
		r.Body, r.Err = os.ReadFile(q.Path)
	case "write":
		r.Err = os.WriteFile(q.Path, q.Body, os.FileMode(q.Mode))
	case "rename":
		r.Err = os.Rename(q.Path, q.OtherPath)
	case "remove":
		r.Err = os.Remove(q.Path)
	case "mkdir":
		r.Err = os.MkdirAll(q.Path, os.FileMode(q.Mode))
	case "parse":
		r.Nodes = parseProfileNodes(q.Body)
	case "ensure-yaml":
		r.Body, r.Nodes, r.Err = ensureClashYAML(q.Body)
	case "merge-profiles":
		r.Body, r.Nodes, r.Err = mergeSubscriptionProfiles(q.Parts)
	case "merge-sources":
		r.Body, r.Nodes, r.Err = mergeSubscriptionSourcesForCache(q.Sources)
	case "download":
		r.Body, r.Nodes, r.Info, r.Err = downloadProfileWithInfo(q.URL, q.Proxy)
	case "running":
		r.Bool = e.a.kernelRunningLocked()
	case "proxy-state":
		r.Bool, r.Text, r.Err = e.a.proxyState()
	case "log":
		log.Print(q.Format)
	case "now":
		r.Timestamp = time.Now().Unix()
	default:
		applySettingsServiceState(e.a, s.settingsServiceState)
		defer func() { s.settingsServiceState = settingsStateFromApp(e.a) }()
		switch q.Kind {
		case "prepare-live-config":
			options, err := e.a.kernelConfigOptions(q.Body, true)
			r.Err = err
			if err == nil {
				if e.a.dnsRelay != nil {
					options.dnsRelayAddr = e.a.dnsRelay.listener.Addr().String()
				}
				r.Text = generatedConfig(options)
			}
		case "apply-live-config":
			r.Err = e.a.applyLiveConfig(q.Format)
		case "save-settings":
			r.Err = e.a.saveSettings()
		case "save-nodes":
			r.Err = e.a.saveCachedNodesLocked(q.Nodes)
		case "registry":
			out := e.a.runSubscriptionService(q.Registry)
			r.Err = out.Err
			r.Entry = out.Entry
			r.Found = out.Found
		default:
			panic("unknown profile effect: " + q.Kind)
		}
	}
	return
}

type profileAcquisitionResult struct {
	ID    string
	Body  []byte
	Nodes []proxyNode
	Info  *subscriptionUserInfo
	Err   error
}

// Preserve the original concurrent acquisition barrier and stable result indexing.
func profileAcquireBatch(entries []subscriptionEntry, run func(subscriptionEntry) profileAcquisitionResult) []profileAcquisitionResult {
	results := make([]profileAcquisitionResult, len(entries))
	var wg sync.WaitGroup
	for i, en := range entries {
		wg.Add(1)
		go func(i int, en subscriptionEntry) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					results[i] = profileAcquisitionResult{ID: en.ID, Err: errors.New("profile acquisition interrupted")}
				}
			}()
			results[i] = run(en)
		}(i, en)
	}
	wg.Wait()
	return results
}
