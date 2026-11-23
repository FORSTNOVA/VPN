package main

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"strings"
)

type appLabelsEffects struct {
	a                          *app
	file                       string
	original, rebuilt, patched []byte
	current, meta              map[string]any
	old                        map[string][]map[string]any
	sources                    []SubscriptionSource
	hooks                      map[string]func(labelsEffect) labelsReply
	read                       func(string) ([]byte, error)
	write                      func(string, []byte, os.FileMode) error
	rename                     func(string, string) error
	remove                     func(string) error
	cached                     func() []SubscriptionSource
	merge                      func([]SubscriptionSource) ([]byte, []proxyNode, error)
	index                      func([]byte, []manualNode) (map[string][]map[string]any, error)
	decode                     func([]byte, any) error
	encode                     func(any) ([]byte, error)
	save                       func([]proxyNode) error
}

func newAppLabelsEffects(a *app) *appLabelsEffects {
	return &appLabelsEffects{a: a, read: os.ReadFile, write: os.WriteFile, rename: os.Rename, remove: os.Remove, cached: a.cachedSources, merge: mergeSubscriptionSources, index: proxyIndex, decode: decodeLabelDocument, encode: yaml.Marshal, save: a.saveCachedNodesLocked}
}
func (e *appLabelsEffects) Release() {}
func (e *appLabelsEffects) snapshot(kind string) *labelsState {
	a := e.a
	s := &labelsState{Merged: a.controlMetaLocked().SubscriptionURL == mergedSubscriptionURL, Empty: a.controlMetaLocked().SubscriptionURL == "", DefaultLabel: nodeQueryDefaultLabel()}
	if kind == "single-snapshot" || kind == "sources-snapshot" {
		for _, v := range a.catalogEntriesLocked() {
			s.Sources = append(s.Sources, labelsSource{Enabled: v.Enabled, Nonempty: strings.TrimSpace(v.URL) != "", Matches: v.URL == a.controlMetaLocked().SubscriptionURL, Label: v.Label})
		}
	}
	if kind == "single-snapshot" || kind == "nodes-snapshot" {
		for _, n := range a.cachedNodeSnapshotLocked() {
			s.Nodes = append(s.Nodes, labelsNode{Name: n.Name, Label: n.SourceLabel, Manual: n.Manual})
		}
	}
	return s
}
func (e *appLabelsEffects) Call(q labelsEffect) (r labelsReply) {
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	switch q.Kind {
	case "mode", "single-snapshot", "sources-snapshot", "nodes-snapshot":
		r.State = e.snapshot(q.Kind)
	case "read-original":
		e.file = e.a.profilePath()
		e.original, r.Err = e.read(e.file)
	case "decode-original":
		r.Err = e.decode(e.original, &e.current)
		r.Bool = e.current != nil
		_, r.Other = e.current["smartvpn-policy-sections"]
	case "cached-sources":
		e.sources = e.cached()
		r.Count = len(e.sources)
	case "rebuild":
		e.rebuilt, _, r.Err = e.merge(e.sources)
	case "index-original":
		e.old, r.Err = e.index(e.original, nil)
	case "index-rebuilt":
		var fresh map[string][]map[string]any
		fresh, r.Err = e.index(e.rebuilt, nil)
		r.Bool = reflect.DeepEqual(e.old, fresh)
	case "decode-meta":
		r.Err = e.decode(e.rebuilt, &e.meta)
	case "key-facts":
		_, r.Bool = e.current[q.Name]
		r.Other = e.meta[q.Name] != nil
	case "patch-key":
		e.current[q.Name] = e.meta[q.Name]
	case "marshal":
		e.patched, r.Err = e.encode(e.current)
	case "write-temp":
		r.Err = e.write(e.file+".repair.tmp", e.patched, 0600)
	case "rename":
		r.Err = e.rename(e.file+".repair.tmp", e.file)
	case "remove-temp":
		r.Err = e.remove(e.file + ".repair.tmp")
	case "meta-label":
		labels, _ := e.meta["smartvpn-source-labels"].(map[string]any)
		r.Text = stringValue(labels[q.Name])
	case "set-label":
		e.a.setCachedNodeLabelLocked(q.Index, q.Text)
	case "save-cache":
		r.Err = e.save(e.a.borrowCachedNodesLocked())
	default:
		panic(fmt.Sprintf("unknown labels effect %q", q.Kind))
	}
	return
}
func runAppLabelsService(a *app) labelsServiceResult {
	e := newAppLabelsEffects(a)
	defer e.Release()
	return runSelectedLabelsService(labelsServiceRequest{Op: "restore"}, e)
}
