//go:build windows && rustcore

package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func queryDiffRust(t *testing.T) func(nodeQueryServiceRequest, nodeQueryServiceEffects) nodeQueryServiceResult {
	t.Helper()
	if !rustNodeQueryFlowAvailable() {
		t.Fatal("actual node query DLL unavailable")
	}
	return func(q nodeQueryServiceRequest, f nodeQueryServiceEffects) nodeQueryServiceResult {
		n := nodeQueryFlowRecoveries.Load()
		r, used := runRustNodeQueryService(q, f)
		if !used || nodeQueryFlowRecoveries.Load() != n {
			t.Fatalf("op=%s used=%v recovery=%d reason=%v", q.Op, used, nodeQueryFlowRecoveries.Load()-n, nodeQueryFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestMigrationNodeQueryActualMatrix(t *testing.T) {
	for _, op := range []string{"nodes", "list", "latency", "node-latency", "wait", "delay"} {
		for m := 0; m < 64; m++ {
			t.Run(fmt.Sprintf("%s/%d", op, m), func(t *testing.T) {
				s := queryDiffBase()
				s.Running = m&1 != 0
				s.ReadFail = m&2 != 0
				s.GroupFail = m&4 != 0
				s.SubFail = m&8 != 0
				s.ManualFail = m&16 != 0
				s.RequestFail = m&32 != 0
				queryDiffCompare(t, queryDiffRequest(op), s, queryDiffRust(t))
			})
		}
	}
}
func TestMigrationNodeQueryActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(260000 + i)))
			s := queryDiffBase()
			s.Running = r.Intn(3) != 0
			s.ReadFail = r.Intn(3) == 0
			s.GroupFail = r.Intn(4) == 0
			s.SubFail = r.Intn(3) == 0
			s.ManualFail = r.Intn(3) == 0
			s.RequestFail = r.Intn(4) == 0
			s.ProxyEnabled = r.Intn(2) == 0
			s.WaitEmpty = r.Intn(2) == 0
			s.Edit = r.Intn(3) == 0
			s.Selected = []string{"a", "b", "", autoGroup}[r.Intn(4)]
			s.AfterDelay = []string{"", "a", autoGroup, fallbackGroup}[r.Intn(4)]
			s.Status = []int{200, 204, 500}[r.Intn(3)]
			s.Body = []string{`{"a":12,"b":6}`, `{"a":-1}`, `null`, `{}`, `{invalid`}[r.Intn(5)]
			s.ClockStep = time.Duration(r.Intn(3)) * time.Millisecond
			q := queryDiffRequest([]string{"nodes", "list", "latency", "node-latency", "wait", "delay"}[r.Intn(6)])
			q.Timeout = []time.Duration{-1, 0, time.Millisecond, 120 * time.Millisecond}[r.Intn(4)]
			if q.Op == "list" || q.Op == "delay" || q.Op == "wait" {
				q.Borrowed = r.Intn(2) == 0
			}
			queryDiffCompare(t, q, s, queryDiffRust(t))
		})
	}
}
func TestMigrationNodeQueryActualGoldens(t *testing.T) { queryLegacyGoldens(t, queryDiffRust(t)) }
func TestMigrationNodeQueryActualLegacyMerge(t *testing.T) {
	run := queryDiffRust(t)
	for i := 0; i < 100; i++ {
		r := rand.New(rand.NewSource(int64(262000 + i)))
		s := queryDiffBase()
		s.ReadFail = r.Intn(3) == 0
		s.Selected = []string{"a", "b", autoGroup, ""}[r.Intn(4)]
		s.Parsed = append(s.Parsed, proxyNode{Name: "b", Network: "last-wins", SourceLabel: "explicit", Manual: r.Intn(2) == 0})
		for _, provider := range []string{"SmartVPNSubscription", manualProviderName} {
			items := []any{}
			for j := 0; j < 20; j++ {
				switch r.Intn(5) {
				case 0:
					items = append(items, "invalid-item")
				case 1:
					items = append(items, map[string]any{"name": 7})
				default:
					items = append(items, map[string]any{"name": []string{"a", "b", "x", "", "公告"}[r.Intn(5)], "type": fmt.Sprint(j), "network": "raw", "alive": r.Intn(2) == 0})
				}
			}
			s.Providers[provider] = map[string]any{"proxies": items}
		}
		q := queryDiffRequest("list")
		f := queryDiffNew(q, s)
		details := map[string]proxyNode{}
		if !s.ReadFail {
			parsed := append([]proxyNode(nil), s.Parsed...)
			f.a.labelSingleSource(parsed)
			for _, n := range parsed {
				details[n.Name] = n
			}
		}
		for n, v := range manualNodeDetails(f.a.manualNodes) {
			details[n] = v
		}
		want := migrationOriginalLiveNodeMerge(s.Selected, details, s.Providers)
		actual := run(q, f)
		f.Release()
		if actual.Err != nil || !reflect.DeepEqual(actual.Nodes, want) {
			t.Fatalf("legacy merge%d got=%#v want=%#v err=%v", i, actual.Nodes, want, actual.Err)
		}
	}
}
