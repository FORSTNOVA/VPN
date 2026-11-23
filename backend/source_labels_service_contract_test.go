package main

import (
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"testing"
)

func labelsAdapterGolden(t *testing.T, run labelsDiffRun) {
	for _, renameFail := range []bool{false, true} {
		a, e, file := labelsAdapterFixture(t)
		before, _ := os.ReadFile(file)
		backing := a.cachedNodes
		saves := 0
		e.save = func(n []proxyNode) error {
			saves++
			if &n[0] != &backing[0] {
				t.Fatal("cache source alias lost")
			}
			return errors.New("ignored")
		}
		if renameFail {
			e.rename = func(string, string) error { return errors.New("rename blocked") }
		}
		r := run(labelsServiceRequest{Op: "restore"}, e)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		after, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if renameFail {
			if saves != 0 || !reflect.DeepEqual(before, after) || backing[0].SourceLabel != "old" {
				t.Fatal("rename error changed committed state")
			}
		} else {
			var m map[string]any
			if yaml.Unmarshal(after, &m) != nil {
				t.Fatal("invalid output")
			}
			if m["dns"] != nil || m["custom"] != "preserved" || saves != 1 || backing[0].SourceLabel != "restored" || backing[1].SourceLabel != "" || backing[2].SourceLabel != "keep" {
				t.Fatal("patch/cache semantics changed")
			}
		}
		if _, err = os.Stat(file + ".repair.tmp"); !os.IsNotExist(err) {
			t.Fatal("tmp remains")
		}
	}
}
func TestMigrationLabelsSafeAdapterGolden(t *testing.T) {
	labelsAdapterGolden(t, runSelectedLabelsService)
}
