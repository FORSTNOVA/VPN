package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func chinaWorkflowGoldens(t *testing.T, run chinaTestRun, factory authorityFactory) {
	t.Run("cache-policy", func(t *testing.T) {
		for _, tc := range []struct {
			mode  chinaListMode
			age   time.Duration
			fetch bool
		}{{chinaListCached, chinaIPListTTL + time.Hour, false}, {chinaListIfOverdue, chinaIPListTTL, false}, {chinaListIfOverdue, chinaIPListTTL + time.Nanosecond, true}, {chinaListIfOverdue, -time.Hour, false}, {chinaListNow, 0, true}} {
			s := chinaTestBase()
			s.Age = tc.age
			f := chinaTestNew(t, s, factory)
			r := run(chinaServiceRequest{Op: "ensure", Mode: tc.mode}, f)
			count := 0
			for _, e := range f.Trace {
				if e.Kind == "fetch" {
					count++
				}
			}
			if !r.Status.Available || (count > 0) != tc.fetch || r.Status.Entries != 1000 {
				t.Fatal("cache policy", tc, r.Status, count)
			}
			if !tc.fetch && r.Status.UpdatedAt != f.Now.Add(-tc.age).Format(time.RFC3339) {
				t.Fatal("mtime changed")
			}
		}
	})
	t.Run("mirror-no-mixing", func(t *testing.T) {
		s := chinaTestBase()
		s.Fail["fetch:m1/china6.txt"] = true
		f := chinaTestNew(t, s, factory)
		r := run(chinaServiceRequest{Op: "download"}, f)
		var got []string
		for _, e := range f.Trace {
			if e.Kind == "fetch" {
				got = append(got, e.Name)
			}
		}
		if !reflect.DeepEqual(got, []string{"m1/china.txt", "m1/china6.txt", "m2/china.txt", "m2/china6.txt"}) || r.Source != "m2/" || len(strings.Split(strings.TrimSpace(string(r.Body)), "\n")) != 2000 {
			t.Fatal("mirror or combined duplicates", got, r.Source)
		}
	})
	t.Run("each-file-minimum", func(t *testing.T) {
		s := chinaTestBase()
		for key := range s.Fetch {
			s.Fetch[key] = chinaListBody(500)
		}
		f := chinaTestNew(t, s, factory)
		r := run(chinaServiceRequest{Op: "download"}, f)
		if r.Err == nil {
			t.Fatal("perfile minimum")
		}
		for _, e := range f.Trace {
			if e.Kind == "fetch" && strings.Contains(e.Name, "china6") {
				t.Fatal("failed first file fetched second")
			}
		}
	})
	t.Run("write-cleanup", func(t *testing.T) {
		for _, where := range []string{"mkdir", "write-temp", "rename"} {
			s := chinaTestBase()
			s.Fail[where] = true
			f := chinaTestNew(t, s, factory)
			r := run(chinaServiceRequest{Op: "write", Ranges: []string{"a", "b"}}, f)
			if r.Err == nil {
				t.Fatal("write failure lost")
			}
			k := chinaTraceKinds(f)
			n := map[string]int{"mkdir": 1, "write-temp": 2, "rename": 3}[where]
			if len(k) != n {
				t.Fatal("extra cleanup/effect", k)
			}
			if n >= 2 && string(f.Trace[1].Body) != "a\nb\n" {
				t.Fatal("body changed")
			}
		}
	})
	t.Run("fallback-errors", func(t *testing.T) {
		for _, where := range []string{"fetch:m1/china.txt", "write-temp"} {
			s := chinaTestBase()
			s.Fail[where] = true
			if strings.HasPrefix(where, "fetch") {
				s.Fail["fetch:m2/china.txt"] = true
			}
			f := chinaTestNew(t, s, factory)
			r := run(chinaServiceRequest{Op: "ensure", Mode: chinaListNow}, f)
			if !r.Status.Available || r.Status.Entries != 1000 || r.Status.Error == "" || r.Status.Source != "" {
				t.Fatal("fallback", r.Status)
			}
		}
	})
	t.Run("probe-cleanup", func(t *testing.T) {
		for _, where := range []string{"free-port", "probe-mkdir", "probe-list-write", "probe-config-write", "probe-check", ""} {
			s := chinaTestBase()
			s.Fail[where] = where != ""
			f := chinaTestNew(t, s, factory)
			r := run(chinaServiceRequest{Op: "probe"}, f)
			k := chinaTraceKinds(f)
			cleanup := where != "free-port" && where != "probe-mkdir"
			if r.Flag != (where == "") {
				t.Fatal("probe outcome")
			}
			if cleanup {
				if len(k) < 2 || k[len(k)-2] != "probe-remove-list:" || k[len(k)-1] != "probe-remove-config:" {
					t.Fatal("cleanup order", k)
				}
			} else {
				for _, x := range k {
					if strings.Contains(x, "remove") {
						t.Fatal("premature cleanup", k)
					}
				}
			}
		}
	})
	t.Run("one-probe", func(t *testing.T) {
		s := chinaTestBase()
		s.Fail["probe-check"] = true
		f := chinaTestNew(t, s, factory)
		r := run(chinaServiceRequest{Op: "supported"}, f)
		if r.Flag || !f.a.chinaStateLocked().Probed {
			t.Fatal("failed probe not cached")
		}
		n := len(f.Trace)
		f.s.Fail["probe-check"] = false
		r = run(chinaServiceRequest{Op: "supported"}, f)
		if r.Flag || len(f.Trace) != n+1 {
			t.Fatal("unexpected retry")
		}
	})
	t.Run("fake-worker", func(t *testing.T) {
		s := chinaTestBase()
		s.Initial.Active = true
		s.Initial.Probed = true
		f := chinaTestNew(t, s, factory)
		r := run(chinaServiceRequest{Op: "start-refresh", Mode: chinaListNow}, f)
		r2 := run(chinaServiceRequest{Op: "start-refresh", Mode: chinaListNow}, f)
		if !r.Flag || r2.Flag || len(f.Spawns) != 1 || f.Owned {
			t.Fatal("duplicate spawn/lock")
		}
		run(chinaServiceRequest{Op: "refresh", Mode: f.Spawns[0]}, f)
		c := f.a.chinaStateLocked()
		if c.Fetching || !c.Active || !c.Probed || !c.Status.Available {
			t.Fatal("completion stomped state", c)
		}
	})
	t.Run("options-priority", func(t *testing.T) {
		s := chinaTestBase()
		s.Fail["stat"] = true
		s.Options.TUN = true
		s.Options.DomesticWeb = true
		s.Import = true
		s.Fail["options-import"] = true
		f := chinaTestNew(t, s, factory)
		r := run(chinaServiceRequest{Op: "options"}, f)
		if r.Err == nil || r.Err.Error() != "synthetic options-import" || !r.Options.SourceRuleOrder {
			t.Fatal("import priority", r.Err)
		}
		for _, e := range f.Trace {
			if e.Kind == "free-port" || e.Kind == "options-chain" || e.Kind == "fetch" {
				t.Fatal("unsafe priority effect", e.Kind)
			}
		}
	})
}
func TestChinaServiceIndependentGoldens(t *testing.T) {
	chinaWorkflowGoldens(t, runSelectedChinaService, newSelectedServiceStateBackend)
}

func TestChinaServiceIndependentParseFallback(t *testing.T) {
	chinaParseFallbackGolden(t, runSelectedChinaService, newSelectedServiceStateBackend)
}
func chinaParseFallbackGolden(t *testing.T, run chinaTestRun, factory authorityFactory) {
	s := chinaTestBase()
	s.Override = true
	s.OverrideBody = []byte("<html>bad</html>")
	f := chinaTestNew(t, s, factory)
	r := run(chinaServiceRequest{Op: "ensure", Mode: chinaListNow}, f)
	if !r.Status.Available || r.Status.Error != "" || r.Status.Entries != 1000 {
		t.Fatal("parse fallback must silently retain cache", r.Status)
	}
	for _, e := range f.Trace {
		if e.Kind == "sources" || e.Kind == "fetch" || e.Kind == "write-temp" {
			t.Fatal("override source unexpectedly downloaded or written")
		}
	}
}
func chinaParseGolden(t *testing.T, run chinaTestRun, factory authorityFactory) {
	body := append(chinaListBody(1000), []byte("  # comment\n\t10.0.0.99/24 # hostbits duplicate\n2001:db8::123/32\n\n")...)
	f := chinaTestNew(t, chinaTestBase(), factory)
	r := run(chinaServiceRequest{Op: "parse", Body: body}, f)
	if r.Err != nil || len(r.Ranges) != 1001 || r.Ranges[len(r.Ranges)-1] != "2001:db8::/32" {
		t.Fatal("normalization golden", r.Err, len(r.Ranges))
	}
	n := 0
	for i, x := range r.Ranges {
		if x == "10.0.0.0/24" {
			n++
		}
		if i > 0 && r.Ranges[i-1] > x {
			t.Fatal("not lexical sorted")
		}
	}
	if n != 1 {
		t.Fatal("duplicate count", n)
	}
	for _, tc := range []struct {
		body []byte
		want string
	}{{chinaListBody(999), "only 999 address ranges, expected at least 1000"}, {[]byte("bad/3\n"), "not an address range: \"bad/3\""}} {
		f = chinaTestNew(t, chinaTestBase(), factory)
		r = run(chinaServiceRequest{Op: "parse", Body: tc.body}, f)
		if r.Err == nil || r.Err.Error() != tc.want {
			t.Fatal("parse error priority", r.Err)
		}
	}
}
func TestChinaServiceIndependentParse(t *testing.T) {
	chinaParseGolden(t, runSelectedChinaService, newSelectedServiceStateBackend)
}
func chinaOptionsGolden(t *testing.T, run chinaTestRun, factory authorityFactory) {
	for _, domestic := range []bool{false, true} {
		s := chinaTestBase()
		s.Fail["stat"] = true
		s.Options.TUN = true
		s.Options.DomesticWeb = domestic
		f := chinaTestNew(t, s, factory)
		r := run(chinaServiceRequest{Op: "options"}, f)
		chain, domains := 0, 0
		for _, e := range f.Trace {
			switch e.Kind {
			case "options-chain":
				chain++
			case "options-domains":
				domains++
			case "free-port", "fetch":
				t.Fatal("missing cached options probed/fetched")
			}
		}
		if domestic {
			if r.Err == nil || chain != 0 || domains != 0 {
				t.Fatal("TUN domestic rejection priority")
			}
		} else if r.Err != nil || chain != 1 || domains != 1 {
			t.Fatal("non-domestic TUN incorrectly rejected")
		}
	}
	s := chinaTestBase()
	s.Initial.Probed = true
	s.Initial.RuleOK = false
	f := chinaTestNew(t, s, factory)
	r := run(chinaServiceRequest{Op: "options"}, f)
	if r.Err != nil || r.Options.ChinaDirect {
		t.Fatal("unsupported rule accepted")
	}
	for _, e := range f.Trace {
		if e.Kind == "free-port" {
			t.Fatal("cached unsupported probed again")
		}
	}
}
func TestChinaServiceIndependentOptions(t *testing.T) {
	chinaOptionsGolden(t, runSelectedChinaService, newSelectedServiceStateBackend)
}
