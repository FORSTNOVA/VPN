package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type labelsDiffSpec struct {
	State                labelsState
	Fail                 string
	Count                int
	Valid, Policy, Equal bool
	Existing, Meta       map[string]bool
	Labels               map[string]string
}
type labelsDiffFrame struct {
	Effect          labelsEffect
	State           labelsState
	Patched         []string
	Temp, Installed bool
}
type labelsDiffFX struct {
	spec            labelsDiffSpec
	state           labelsState
	trace           []labelsDiffFrame
	patched         []string
	temp, installed bool
	releases        int
}

func labelsDiffBase() labelsDiffSpec {
	return labelsDiffSpec{State: labelsState{Merged: true, DefaultLabel: "default", Sources: []labelsSource{{Enabled: true, Nonempty: true, Matches: true, Label: "source"}}, Nodes: []labelsNode{{Name: "a"}, {Name: "b", Label: "old"}, {Name: "manual", Manual: true, Label: "own"}}}, Count: 1, Valid: true, Equal: true, Existing: map[string]bool{"dns": true}, Meta: map[string]bool{"proxy-groups": true, "smartvpn-source-labels": true, "dns": true}, Labels: map[string]string{"a": "new-a"}}
}
func labelsDiffNew(s labelsDiffSpec) *labelsDiffFX {
	return &labelsDiffFX{spec: s, state: *cloneLabelsState(&s.State)}
}
func (f *labelsDiffFX) Release() { f.releases++ }
func (f *labelsDiffFX) Call(e labelsEffect) (r labelsReply) {
	defer func() {
		f.trace = append(f.trace, labelsDiffFrame{e, *cloneLabelsState(&f.state), append([]string(nil), f.patched...), f.temp, f.installed})
	}()
	if e.Kind == f.spec.Fail {
		r.Err = errors.New("synthetic " + e.Kind)
		return
	}
	switch e.Kind {
	case "mode", "single-snapshot", "sources-snapshot", "nodes-snapshot":
		r.State = cloneLabelsState(&f.state)
	case "read-original", "rebuild", "index-original", "decode-meta", "marshal", "save-cache":
	case "decode-original":
		r.Bool = f.spec.Valid
		r.Other = f.spec.Policy
	case "cached-sources":
		r.Count = f.spec.Count
	case "index-rebuilt":
		r.Bool = f.spec.Equal
	case "key-facts":
		r.Bool = f.spec.Existing[e.Name]
		r.Other = f.spec.Meta[e.Name]
	case "patch-key":
		f.patched = append(f.patched, e.Name)
	case "write-temp":
		f.temp = true
	case "rename":
		f.temp = false
		f.installed = true
	case "remove-temp":
		f.temp = false
	case "meta-label":
		r.Text = f.spec.Labels[e.Name]
	case "set-label":
		f.state.Nodes[e.Index].Label = e.Text
	default:
		panic("unmocked labels port " + e.Kind)
	}
	return
}

type labelsDiffRun func(labelsServiceRequest, labelsServiceEffects) labelsServiceResult

func labelsDiffCompare(t *testing.T, s labelsDiffSpec, run labelsDiffRun) {
	t.Helper()
	g, r := labelsDiffNew(s), labelsDiffNew(s)
	a := runGoLabelsService(labelsServiceRequest{Op: "restore"}, g)
	b := run(labelsServiceRequest{Op: "restore"}, r)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(g.trace, r.trace) || !reflect.DeepEqual(g.state, r.state) || g.releases != 1 || r.releases != 1 {
		t.Fatalf("labels mismatch fail=%s\ngo=%+v\nrust=%+v\ngo trace=%+v\nrust trace=%+v", s.Fail, a, b, g.trace, r.trace)
	}
}
func labelsDiffGolden(t *testing.T, run labelsDiffRun) {
	t.Helper()
	for i := 0; i < 8; i++ {
		s := labelsDiffBase()
		switch i {
		case 0:
			s.State.Merged = false
		case 1:
			s.State.Merged = false
			s.State.Empty = true
		case 2:
			s.Fail = "rename"
		case 3:
			s.Fail = "write-temp"
		case 4:
			s.Fail = "save-cache"
		case 5:
			s.Count = 2
		case 6:
			s.Policy = true
		case 7:
			s.Existing = map[string]bool{"dns": true, "proxy-groups": true}
		}
		f := labelsDiffNew(s)
		run(labelsServiceRequest{Op: "restore"}, f)
		switch i {
		case 0:
			if f.state.Nodes[0].Label != "source" || f.state.Nodes[1].Label != "old" || f.state.Nodes[2].Label != "own" {
				t.Fatal("single source overwrite")
			}
		case 1:
			if len(f.trace) != 1 {
				t.Fatal("empty should only read mode")
			}
		case 2:
			if f.temp || f.installed || f.state.Nodes[0].Label != "" || f.trace[len(f.trace)-1].Effect.Kind != "remove-temp" {
				t.Fatal("rename compensation")
			}
		case 3:
			if f.installed || f.state.Nodes[0].Label != "" {
				t.Fatal("write failure mutated cache")
			}
		case 4:
			if !f.installed || f.state.Nodes[0].Label != "new-a" || f.state.Nodes[1].Label != "" || f.state.Nodes[2].Label != "own" {
				t.Fatal("save failure rollback or manual label")
			}
		case 5, 6:
			if f.installed {
				t.Fatal("early guard ignored")
			}
		case 7:
			if !reflect.DeepEqual(f.patched, []string{"smartvpn-source-labels"}) {
				t.Fatalf("existing keys overwritten %v", f.patched)
			}
		}
		if f.releases != 1 {
			t.Fatal(fmt.Sprint("releases ", f.releases))
		}
	}
}
func TestMigrationLabelsSafeSelectedLifecycle(t *testing.T) {
	for _, merged := range []bool{false, true} {
		s := labelsDiffBase()
		s.State.Merged = merged
		labelsDiffCompare(t, s, runSelectedLabelsService)
	}
	labelsDiffGolden(t, runSelectedLabelsService)
}
