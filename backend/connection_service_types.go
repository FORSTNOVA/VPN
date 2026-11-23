package main

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"time"
)

type connectionServiceRequest struct{ Op, Name string }
type connectionServiceResult struct {
	Status  int
	Message string
	Data    map[string]any
	Err     error
}
type connectionServiceState struct {
	Chain, ProxyOn, StoreAvailable                                      bool
	Mode, SelectionMode, SelectedNode, LockedRegion, LockedNode, Choice string
	CachedNodes                                                         []proxyNode
}
type connectionEffect struct {
	Kind, Name, Path, Mutation, Region, Mode string
	Nodes                                    []proxyNode
	Bool                                     bool
}
type connectionReply struct {
	State   *connectionServiceState
	Bool    bool
	Err     error
	Nodes   []proxyNode
	Strings []string
	Value   any
	Region  regionRecord
	Found   bool
	Latency int
}
type connectionServiceEffects interface {
	Call(connectionEffect) connectionReply
	Release()
}

func connectionStateFromApp(a *app) *connectionServiceState {
	return &connectionServiceState{Chain: a.chainEnabledLocked(), ProxyOn: a.proxyOn, StoreAvailable: a.store != nil, Mode: a.connectionModeLocked(), SelectionMode: a.controlMetaLocked().SelectionMode, SelectedNode: a.controlMetaLocked().SelectedNode, LockedRegion: a.controlMetaLocked().RuntimeLockedRegion, LockedNode: a.controlMetaLocked().RuntimeLockedNode, Choice: a.selectedChoice(), CachedNodes: a.cachedNodeSnapshotLocked()}
}

type appConnectionEffects struct {
	a             *app
	locked, owns  bool
	connecting    bool
	resumeLast    bool
	resumeChoice  string
	connectFiles  []priorFile
	pendingUpsert []proxyNode
	selecting     bool
	before        serviceControlState
	cached        []proxyNode
	previousGroup string
	saveErr       error
}

func (e *appConnectionEffects) Release() {
	if e.owns {
		e.owns = false
		e.locked = false
		e.a.mu.Unlock()
	}
}
func (e *appConnectionEffects) Call(q connectionEffect) (r connectionReply) {
	if q.Kind == "lock" {
		if e.locked {
			panic("connection lock already held")
		}
		if !e.owns {
			e.a.mu.Lock()
			e.owns = true
			if e.selecting || e.connecting {
				e.before = e.a.controlMetaLocked()
				e.cached = e.a.borrowCachedNodesLocked()
			}
		}
		e.locked = true
		r.State = connectionStateFromApp(e.a)
		return
	}
	if q.Kind == "unlock" {
		if !e.owns {
			panic("connection cannot unlock borrowed lock")
		}
		if e.selecting {
			e.locked = false
		} else {
			e.Release()
		}
		return
	}
	// Unlocked selection only performs list/PUT. It must not snapshot or write stale state.
	if !e.locked && q.Kind != "list" && q.Kind != "put" {
		panic("connection effect requires lock: " + q.Kind)
	}
	defer func() {
		if e.locked {
			r.State = connectionStateFromApp(e.a)
			if e.resumeChoice != "" {
				r.State.Choice = e.resumeChoice
			}
		}
	}()
	switch q.Kind {
	case "snapshot":
	case "running":
		r.Bool = e.a.kernelRunningLocked()
	case "proxy-conflict":
		r.Bool = e.a.proxyServedByAnotherInstance()
	case "start-kernel":
		if e.connecting {
			e.connectFiles, r.Err = captureFiles(filepath.Join(e.a.home, "settings.json"), e.a.cachedNodesPath())
			if r.Err != nil {
				return
			}
		}
		r.Err = e.a.startMihomoLocked()
	case "stop-kernel":
		if e.a.rememberingNode && e.a.kernelRunningLocked() {
			e.a.activeNodeLocked()
		}
		e.a.rememberingNode = false
		r.Err = e.a.stopMihomoLocked()
		if e.connecting && e.connectFiles != nil {
			e.a.patchControlLocked(settingsClearPinsMask, e.before)
			e.a.replaceCachedNodesLocked(e.cached, false)
			if err := restoreFiles(e.connectFiles); err != nil {
				e.saveErr = err
			}
		}
	case "list":
		if e.owns || e.locked {
			r.Nodes, r.Err = e.a.listNodesLocked()
			if e.resumeLast && r.Err == nil && e.a.store != nil && !e.a.chainEnabledLocked() {
				control := e.a.controlMetaLocked()
				if control.SelectionMode != "manual" && control.RuntimeLockedRegion == "" {
					name, found, err := e.a.store.getValue("lastConnectedNode")
					if err == nil && found && connectionNodeExists(r.Nodes, name) {
						e.resumeChoice = name
					}
				}
			}
		} else {
			r.Nodes, r.Err = e.a.listNodes()
		}
	case "put":
		if e.selecting {
			old, err := e.a.mihomoRequest(http.MethodGet, q.Path, nil)
			if err != nil {
				r.Err = err
				return
			}
			e.previousGroup, _ = old["now"].(string)
			if e.previousGroup == "" {
				r.Err = errors.New("missing current Mihomo selection")
				return
			}
		}
		_, r.Err = e.a.mihomoRequest(http.MethodPut, q.Path, map[string]string{"name": q.Name})
	case "get":
		var v map[string]any
		v, r.Err = e.a.mihomoRequest(http.MethodGet, q.Path, nil)
		r.Value = v["now"]
	case "save-settings":
		r.Err = e.a.saveSettings()
		if e.selecting && r.Err != nil {
			e.saveErr = r.Err
			e.a.patchControlLocked(controlSelectionMode|controlSelectedNode|controlLockedRegion|controlLockedNode|controlRuntimeLockedRegion|controlRuntimeLockedNode, e.before)
			e.a.replaceCachedNodesLocked(e.cached, false)
			if e.previousGroup != "" {
				e.saveErr = errors.Join(e.saveErr, e.a.switchGroupLocked(e.previousGroup))
			}
		}
	case "save-cache":
		r.Err = e.a.saveCachedNodesLocked(q.Nodes)
	case "upsert":
		if e.connecting {
			e.pendingUpsert = q.Nodes
		} else {
			r.Err = e.a.store.upsertNodes(q.Nodes)
		}
	case "region":
		r.Region, r.Found = e.a.regionRecordLocked(q.Name)
	case "region-fresh":
		r.Bool = time.Since(e.a.regionValueLocked(q.Name).VerifiedAt) <= regionTTL
	case "candidates":
		r.Strings = candidatesForRegion(e.a.controlMetaLocked().RuntimeLockedRegion, e.a.healthDomainLocked().Regions, e.a.healthDomainLocked().Health, time.Now())
	case "latency":
		r.Latency = e.a.healthValueLocked(q.Name).LatencyMs
	case "system-proxy":
		r.Err = e.a.setSystemProxy(q.Bool)
	case "start-patrol":
		e.a.rememberingNode = true
		e.a.activeNodeLocked()
		if len(e.pendingUpsert) > 0 {
			if err := e.a.store.upsertNodes(e.pendingUpsert); err != nil {
				log.Printf("could not persist the node list: %v", err)
			}
		}
		e.a.startPatrolLocked()
	case "start-path":
		e.a.startPathMonitorLocked()
	case "async-patrol":
		go e.a.patrolOnce()
	case "stop-patrol":
		e.a.stopPatrolLocked()
	case "stop-path":
		e.a.stopPathMonitorLocked()
	case "log":
		log.Print(q.Name)
	case "commit":
		switch q.Mutation {
		case "lock-node":
			e.a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: q.Name})
			e.a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: q.Name})
		case "selection":
			e.a.patchControlLocked(controlSelectionMode, serviceControlState{SelectionMode: q.Mode})
			e.a.patchControlLocked(controlSelectedNode, serviceControlState{SelectedNode: q.Name})
		case "cache":
			e.a.replaceCachedNodesLocked(cloneConnectionNodes(q.Nodes), false)
		case "region":
			e.a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: q.Region})
			e.a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: q.Name})
			e.a.patchControlLocked(controlLockedRegion, serviceControlState{LockedRegion: q.Region})
			e.a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: q.Name})
		case "clear-block":
			e.a.setHealthBlockedLocked(false)
			e.a.setHealthReasonLocked("")
		case "proxy-on":
			e.a.proxyOn = q.Bool
		default:
			panic("unknown connection mutation: " + q.Mutation)
		}
	default:
		panic("unknown connection effect: " + q.Kind)
	}
	return
}
func (a *app) runConnectionService(q connectionServiceRequest) (out connectionServiceResult) {
	return a.connectionService(q, false)
}

func (a *app) connectionService(q connectionServiceRequest, resumeLast bool) (out connectionServiceResult) {
	defer func() {
		if recover() != nil {
			out = connectionServiceResult{Status: 500, Message: "connection operation interrupted", Err: errors.New("connection operation interrupted")}
		}
	}()
	fx := &appConnectionEffects{a: a, locked: q.Op == "drop", selecting: q.Op == "select", connecting: q.Op == "connect", resumeLast: resumeLast}
	defer fx.Release()
	r := runSelectedConnectionService(q, fx)
	if fx.saveErr != nil && fx.connecting {
		return connectionFailure(500, "could not restore prior connection settings")
	}
	if fx.saveErr != nil && r.Status == 200 {
		return connectionFailure(500, "could not save the node selection")
	}
	return r
}
func writeConnectionServiceResult(w http.ResponseWriter, r connectionServiceResult) {
	if r.Status != 200 {
		http.Error(w, r.Message, r.Status)
		return
	}
	writeJSON(w, r.Data)
}

func cloneConnectionNodes(v []proxyNode) []proxyNode {
	if v == nil {
		return nil
	}
	out := make([]proxyNode, len(v))
	copy(out, v)
	return out
}
