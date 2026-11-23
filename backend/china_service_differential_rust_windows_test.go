//go:build windows && rustcore

package main

import (
	"fmt"
	"testing"
	"time"
)

func chinaStrictRun(t *testing.T) chinaTestRun {
	t.Helper()
	if !rustChinaFlowAvailable() {
		t.Fatal("China planner unavailable")
	}
	return func(q chinaServiceRequest, f chinaServiceEffects) chinaServiceResult {
		n := chinaFlowRecoveries.Load()
		r, ok := runRustChinaService(q, f)
		if !ok || chinaFlowRecoveries.Load() != n {
			t.Fatalf("China planner recovered op=%s reason=%v", q.Op, chinaFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestChinaFlowIndependentGoldens(t *testing.T) {
	authorityRequireRust(t)
	run := chinaStrictRun(t)
	chinaWorkflowGoldens(t, run, authorityRustFactory)
	chinaParseFallbackGolden(t, run, authorityRustFactory)
}
func TestChinaFlowIndependentMatrix(t *testing.T) {
	authorityRequireRust(t)
	for _, op := range []string{"read", "ensure", "download", "probe", "supported", "options", "refresh", "start-refresh", "status", "write"} {
		for i := 0; i < 12; i++ {
			t.Run(fmt.Sprintf("%s-%d", op, i), func(t *testing.T) {
				s := chinaTestBase()
				s.Initial = serviceChinaState{Fetching: i%4 == 0, Probed: i%3 == 0, RuleOK: i%2 == 0, Active: i%2 == 1}
				s.Import = i%2 == 0
				s.Options.TUN = i%2 == 0
				s.Options.DomesticWeb = i%3 == 0
				where := []string{"", "stat", "read", "fetch:m1/china.txt", "fetch:m2/china6.txt", "mkdir", "write-temp", "rename", "probe-check", "options-import", "options-chain", "free-port"}[i]
				if where != "" {
					s.Fail[where] = true
				}
				if i%4 == 1 {
					s.Cache = []byte("bad")
				}
				s.Age = time.Duration(i) * 24 * time.Hour
				q := chinaServiceRequest{Op: op, Mode: chinaListMode(i % 3), Ranges: []string{"synthetic"}}
				chinaTestParity(t, q, s, chinaStrictRun(t), authorityRustFactory)
			})
		}
	}
	for i, body := range [][]byte{nil, []byte("bad"), chinaListBody(999), chinaListBody(1000), append(chinaListBody(1000), []byte("# comment\n10.0.0.99/24 # duplicate\n2001:db8::1/32\n")...)} {
		t.Run(fmt.Sprintf("parse-%d", i), func(t *testing.T) {
			chinaTestParity(t, chinaServiceRequest{Op: "parse", Body: body}, chinaTestBase(), chinaStrictRun(t), authorityRustFactory)
		})
	}
}
func TestChinaFlowIndependentParse(t *testing.T) {
	authorityRequireRust(t)
	chinaParseGolden(t, chinaStrictRun(t), authorityRustFactory)
}
func TestChinaFlowIndependentOptions(t *testing.T) {
	authorityRequireRust(t)
	chinaOptionsGolden(t, chinaStrictRun(t), authorityRustFactory)
}
