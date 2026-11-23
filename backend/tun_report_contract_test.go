package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func tunIndependentGolden(t *testing.T, direct tunIndependentRun) {
	run := func(t *testing.T, q tunReportRequest, fx tunReportEffects) tunReportResult {
		defer fx.Release()
		r := direct(t, q, fx)
		if r.Err != nil {
			t.Fatalf("TUN %s failed: %v", q.Op, r.Err)
		}
		return r
	}
	tunIndependentFormatGolden(t, run)
	t.Run("windows-inactive", func(t *testing.T) {
		f := &tunIndependentFX{}
		r := run(t, tunReportRequest{Op: "windows-checks"}, f)
		c := r.Data["checks"].([]tunCheck)
		if r.Data["active"] != false || len(c) != 3 || c[0].State != "fail" || c[1].State != "fail" || c[2].State != "warn" {
			t.Fatal(r)
		}
		if _, ok := r.Data["interface"]; ok {
			t.Fatal("inactive has interface")
		}
		if !reflect.DeepEqual(tunIndependentKinds(f), []string{"lock", "elevated", "wintun-path", "wintun-stat", "running"}) {
			t.Fatal(f.trace)
		}
	})
	t.Run("android-dead-descriptor-still-active", func(t *testing.T) {
		f := &tunIndependentFX{s: tunIndependentSpec{Authorized: true, FD: 3, Alive: false, Running: true, Routes: []string{"::/0"}, DNS: []string{"10.0.0.1"}}}
		r := run(t, tunReportRequest{Op: "android-checks"}, f)
		c := r.Data["checks"].([]tunCheck)
		if r.Data["active"] != true || len(c) != 8 || c[1].State != "fail" || c[2].State != "ok" || c[4].State != "warn" {
			t.Fatal(r)
		}
		want := []string{"refresh", "lock", "tunnel-state", "descriptor-alive", "running", "read-profile", "hostname", "active-node", "region"}
		if !reflect.DeepEqual(tunIndependentKinds(f), want) {
			t.Fatal(f.trace)
		}
	})
	t.Run("android-status-two-observations", func(t *testing.T) {
		f := &tunIndependentFX{s: tunIndependentSpec{FD: 2, Running: true, Mode: "system"}}
		r := run(t, tunReportRequest{Op: "android-status"}, f)
		if r.Data["active"] != true || r.Data["available"] != false || r.Data["descriptor"] != 2 {
			t.Fatal(r)
		}
		if !reflect.DeepEqual(tunIndependentKinds(f), []string{"refresh", "lock", "tunnel-state", "tunnel-state", "mode", "running"}) {
			t.Fatal(f.trace)
		}
	})
	t.Run("path-five-branches", func(t *testing.T) {
		for _, c := range []struct{ a, b, state string }{{"", "", "warn"}, {"1", "", "warn"}, {"", "1", "fail"}, {"1", "1", "ok"}, {"1", "2", "fail"}} {
			f := &tunIndependentFX{s: tunIndependentSpec{Tunnel: exitIPResult{Address: c.a}, System: exitIPResult{Address: c.b}}}
			r := run(t, tunReportRequest{Op: "path", Name: "named"}, f)
			check := r.Data["check"].(tunCheck)
			if check.State != c.state || check.Name != "named" || !reflect.DeepEqual(tunIndependentKinds(f), []string{"exit-tunnel", "exit-system"}) {
				t.Fatalf("case %+v: %+v", c, r)
			}
		}
	})
	t.Run("opposite-fake-ip", func(t *testing.T) {
		for _, op := range []string{"dns", "node"} {
			f := &tunIndependentFX{s: tunIndependentSpec{Host: "node.example", Answers: []string{"8.8.8.8", "198.18.0.7"}}}
			r := run(t, tunReportRequest{Op: op}, f)
			want := "ok"
			if op == "node" {
				want = "fail"
			}
			if r.Data["check"].(tunCheck).State != want {
				t.Fatal(r)
			}
		}
	})
	t.Run("region-address-before-pin", func(t *testing.T) {
		f := &tunIndependentFX{s: tunIndependentSpec{Known: true, Region: regionRecord{ExitIP: "1.2.3.4", Country: "JP"}, Tunnel: exitIPResult{Address: "2.3.4.5"}, Lock: "US"}}
		r := run(t, tunReportRequest{Op: "region"}, f)
		if r.Data["check"].(tunCheck).State != "fail" || !reflect.DeepEqual(tunIndependentKinds(f), []string{"active-node", "region", "exit-tunnel"}) {
			t.Fatal(r, f.trace)
		}
	})
}
func TestTunReportIndependentSelectedGolden(t *testing.T) {
	tunIndependentGolden(t, func(_ *testing.T, q tunReportRequest, fx tunReportEffects) tunReportResult {
		return runSelectedTunReport(q, fx)
	})
}

func tunIndependentFormatGolden(t *testing.T, run tunIndependentRun) {
	t.Run("format-tokens-remain-data", func(t *testing.T) {
		left, right := "first%s/%d/%q/节点", "second%s/%d/%q/出口"
		f := &tunIndependentFX{s: tunIndependentSpec{Tunnel: exitIPResult{Error: left}, System: exitIPResult{Error: right}}}
		r := run(t, tunReportRequest{Op: "path", Name: "custom%s"}, f)
		c := r.Data["check"].(tunCheck)
		if c.State != "warn" || c.Name != "custom%s" || !strings.Contains(c.Detail, left+" / "+right) {
			t.Fatalf("formatted user data was reinterpreted: %+v", c)
		}
	})
	t.Run("node-format-token", func(t *testing.T) {
		host := "%s/%d/%q/节点"
		f := &tunIndependentFX{s: tunIndependentSpec{Host: host, Answers: []string{"8.8.8.8"}}}
		r := run(t, tunReportRequest{Op: "node"}, f)
		c := r.Data["check"].(tunCheck)
		if c.State != "ok" || !strings.HasPrefix(c.Value, host) || !strings.HasSuffix(c.Value, "8.8.8.8") {
			t.Fatal(c)
		}
	})
	t.Run("adapter-go-quote-unicode", func(t *testing.T) {
		status := "%s/%d/%q/网\n\"卡"
		f := &tunIndependentFX{s: tunIndependentSpec{Running: true, Mode: modeTUN, Adapter: adapterReport{Present: true, Status: status}}}
		r := run(t, tunReportRequest{Op: "windows-checks"}, f)
		c := r.Data["checks"].([]tunCheck)
		if len(c) != 8 || c[2].State != "fail" || !strings.Contains(c[2].Detail, strconv.Quote(status)) {
			t.Fatal(c)
		}
	})
}
