//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"os"
	"reflect"
	"testing"
)

func TestPhase89SubscriptionOperations(t *testing.T) {
	for _, tc := range []struct {
		op    uint32
		input string
	}{{7, "total=42"}, {8, "MATCH,old"}} {
		_, _, available := rustSubscriptionCall(tc.op, tc.input)
		if expected := os.Getenv("SMARTVPN_TEST_PROFILE_OPS"); expected != "" && available != (expected == "rust") {
			t.Fatalf("op %d available=%t expected %s", tc.op, available, expected)
		}
	}
	for _, header := range []string{"", "x=bad", "x=+5", "upload=-9223372036854775808;download=9223372036854775807", "TOTAL=3;total=bad;total=4;expire=9223372036854775808", "exp\u0130re=+12", "download\u00a0=\u30007", "\xff=6;total=3"} {
		if got, want := parseSubscriptionUserInfo(header), parseSubscriptionUserInfoGo(header); !reflect.DeepEqual(got, want) {
			t.Fatalf("header %q got %+v want %+v", header, got, want)
		}
	}
	for _, rule := range []string{"", "MATCH", "MATCH,", "A,no-resolve", "DOMAIN,x, \u540d ,\u00a0no-resolve ", "MATCH,DIRECT", "MATCH, REJECT", "MATCH, direct", "RULE-SET,x,\xff "} {
		i, j, ok := mergeRuleTarget(rule)
		wi, wj, wok := mergeRuleTargetGo(rule)
		if i != wi || j != wj || ok != wok {
			t.Fatalf("rule %q got %d,%d,%t want %d,%d,%t", rule, i, j, ok, wi, wj, wok)
		}
	}
}
