package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNormalizePathChecks(t *testing.T) {
	normalized, err := normalizePathChecks(pathCheckSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.IntervalSec != pathCheckDefaultInterval {
		t.Fatalf("interval = %d", normalized.IntervalSec)
	}
	if normalized.Enabled {
		t.Fatal("a background check must be switched on deliberately")
	}
	normalized, _ = normalizePathChecks(pathCheckSettings{IntervalSec: 5})
	if normalized.IntervalSec != pathCheckMinInterval {
		t.Fatalf("a too-short interval must be raised, got %d", normalized.IntervalSec)
	}
	normalized, _ = normalizePathChecks(pathCheckSettings{IntervalSec: 99999})
	if normalized.IntervalSec != pathCheckMaxInterval {
		t.Fatalf("a too-long interval must be capped, got %d", normalized.IntervalSec)
	}

	// The presets are always on offer, and the two defaults are on.
	normalized, err = normalizePathChecks(pathCheckSettings{
		Targets: []pathTarget{{Label: "Mine", URL: "https://example.com/", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Targets) != len(pathCheckPresets)+1 {
		t.Fatalf("got %d targets, want the custom one plus every preset", len(normalized.Targets))
	}
	enabled := map[string]bool{}
	for _, target := range normalized.Targets {
		enabled[target.Label] = target.Enabled
	}
	if !enabled["Google"] || !enabled["YouTube"] {
		t.Fatalf("Google and YouTube are the defaults: %+v", normalized.Targets)
	}
	if enabled["GitHub"] || enabled["Claude"] || enabled["ChatGPT"] || enabled["Twitch"] {
		t.Fatalf("the other presets start switched off: %+v", normalized.Targets)
	}
	if !enabled["Mine"] {
		t.Fatal("a custom target keeps the state it was given")
	}
	// A built-in is recognised by its address, not by what the page claimed.
	for _, target := range normalized.Targets {
		if target.URL == "https://github.com/" && !target.Builtin {
			t.Fatal("a preset address must be marked built-in")
		}
		if target.URL == "https://example.com/" && target.Builtin {
			t.Fatal("a custom address must not be marked built-in")
		}
	}
}

func TestNormalizePathChecksRejections(t *testing.T) {
	for name, target := range map[string]pathTarget{
		"no scheme":    {Label: "x", URL: "example.com"},
		"wrong scheme": {Label: "x", URL: "ftp://example.com/"},
		"no host":      {Label: "x", URL: "https://"},
		"long label":   {Label: strings.Repeat("字", 41), URL: "https://example.com/"},
		"file scheme":  {Label: "x", URL: "file:///c:/x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizePathChecks(pathCheckSettings{Targets: []pathTarget{target}}); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}

	// A label may be left out: the host stands in for it.
	normalized, err := normalizePathChecks(pathCheckSettings{
		Targets: []pathTarget{{URL: "https://example.com/path", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Targets[0].Label != "example.com" {
		t.Fatalf("label = %q", normalized.Targets[0].Label)
	}

	// Duplicates collapse, and the list has a ceiling.
	normalized, err = normalizePathChecks(pathCheckSettings{Targets: []pathTarget{
		{Label: "a", URL: "https://a.example/"},
		{Label: "b", URL: "https://a.example/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Targets) != len(pathCheckPresets)+1 {
		t.Fatalf("the duplicate must collapse: %+v", normalized.Targets)
	}

	many := make([]pathTarget, 0, pathCheckMaxTargets*2)
	for i := 0; i < pathCheckMaxTargets*2; i++ {
		many = append(many, pathTarget{Label: "t", URL: "https://h" + strings.Repeat("x", i+1) + ".example/"})
	}
	if _, err := normalizePathChecks(pathCheckSettings{Targets: many}); err == nil {
		t.Fatal("more targets than the ceiling must be refused, not silently trimmed")
	}
	// The ceiling counts the presets too, so the last accepted list still fits.
	exactly := many[:pathCheckMaxTargets-len(pathCheckPresets)]
	normalized, err = normalizePathChecks(pathCheckSettings{Targets: exactly})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Targets) != pathCheckMaxTargets {
		t.Fatalf("got %d targets, want exactly %d", len(normalized.Targets), pathCheckMaxTargets)
	}
}

func TestPathPassed(t *testing.T) {
	// These sites answer 403 or 429 to anything that is not a browser, and that
	// still means the path works.
	for _, state := range []string{"ok", "redirect", "restricted"} {
		if !pathPassed(siteCheckResult{Name: "x", State: state}) {
			t.Errorf("%q counts as a working path", state)
		}
	}
	for _, state := range []string{"error", "skipped", ""} {
		if pathPassed(siteCheckResult{Name: "x", State: state}) {
			t.Errorf("%q must not count as a working path", state)
		}
	}

	failures := pathFailures([]siteCheckResult{
		{Name: "Google", State: "ok"},
		{Name: "YouTube", State: "error"},
		{Name: "Twitch", State: "restricted"},
		{Name: "Claude", State: "error"},
	})
	if len(failures) != 2 || failures[0] != "YouTube" || failures[1] != "Claude" {
		t.Fatalf("failures = %v", failures)
	}
}

// pathRoundApp is an app with a running kernel, a region and its candidates. The
// stub answers for the group the way the kernel does — including following the
// switches the round makes — so the round can tell which node is in use.
func pathRoundApp(t *testing.T, region string, nodes ...string) (*app, *controllerStub, func() string) {
	t.Helper()
	initial := ""
	if len(nodes) > 0 {
		initial = nodes[0]
	}
	// The stub is needed by the helper and the helper by the stub, so the stub is
	// captured as it is built; its handler only runs once it is serving.
	var stub *controllerStub
	currentChoice := func() string {
		choice := initial
		stub.mu.Lock()
		defer stub.mu.Unlock()
		for _, call := range stub.calls {
			if !strings.HasPrefix(call, http.MethodPut+" /proxies/SmartVPN") {
				continue
			}
			if at := strings.Index(call, `"name":"`); at >= 0 {
				rest := call[at+len(`"name":"`):]
				if end := strings.Index(rest, `"`); end >= 0 {
					choice = rest[:end]
				}
			}
		}
		return choice
	}
	stub = newControllerStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxies/SmartVPN" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"now":"` + currentChoice() + `"}`))
	})
	a := stub.app(t)
	runningKernel(t, a)
	a.lockedRegion = region
	now := time.Now()
	for _, name := range nodes {
		if region != "" {
			a.regions[name] = regionRecord{Name: name, Country: region, Status: regionVerified, VerifiedAt: now}
		}
		a.health[name] = nodeHealth{Name: name, State: healthHealthy, UpdatedAt: now, LastSuccessAt: now}
	}
	a.settings.PathChecks = pathCheckSettings{
		Enabled: true, IntervalSec: 60,
		Targets: []pathTarget{{Label: "YouTube", URL: "https://www.youtube.com/generate_204", Enabled: true}},
	}
	return a, stub, currentChoice
}

// checkerThatFailsOnce fails the first measurement and passes afterwards, which
// is what a switch to a working node looks like from the round's side.
func checkerThatFailsOnce() (pathChecker, *int) {
	attempts := 0
	return func(_ string, targets []pathTarget) []siteCheckResult {
		attempts++
		return pathResults(targets, attempts > 1)
	}, &attempts
}

func checkerAlwaysFailing() pathChecker {
	return func(_ string, targets []pathTarget) []siteCheckResult {
		return pathResults(targets, false)
	}
}

func pathResults(targets []pathTarget, passing bool) []siteCheckResult {
	results := make([]siteCheckResult, 0, len(targets))
	for _, target := range targets {
		state := "error"
		if passing {
			state = "ok"
		}
		results = append(results, siteCheckResult{Name: target.Label, State: state})
	}
	return results
}

func TestPathRoundSwitchesUntilItPasses(t *testing.T) {
	a, stub, choice := pathRoundApp(t, "JP", "jp-a", "jp-b")
	// jp-a is the node in use and it is the one that cannot reach the site.
	a.health["jp-a"] = nodeHealth{Name: "jp-a", State: healthUnhealthy, UpdatedAt: time.Now()}
	check, attempts := checkerThatFailsOnce()

	a.runPathRound(check)

	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"jp-b"`) {
		t.Fatalf("the round should have moved to the candidate, calls: %v", stub.calls)
	}
	if *attempts != 2 {
		t.Fatalf("a switch only counts once the path is re-checked, got %d measurements", *attempts)
	}
	if choice() != "jp-b" {
		t.Fatalf("the group must end on the passing node, got %q", choice())
	}
	if len(a.pathStatus.Failures) != 0 || a.pathStatus.Note != "" {
		t.Fatalf("the round must end clean: %+v", a.pathStatus)
	}
	if a.lockedNode != "jp-b" {
		t.Fatalf("the pinned node follows the switch: %q", a.lockedNode)
	}
}

func TestPathRoundStopsWhenTheRegionRunsOut(t *testing.T) {
	a, stub, _ := pathRoundApp(t, "JP", "jp-a", "jp-b", "jp-c")
	// Nothing passes, so every candidate is tried and then it gives up.
	a.runPathRound(checkerAlwaysFailing())

	if len(a.pathStatus.Failures) == 0 {
		t.Fatalf("the failure must be recorded: %+v", a.pathStatus)
	}
	if !strings.Contains(a.pathStatus.Note, "都通不过") {
		t.Fatalf("note = %q, want it to say the region is exhausted", a.pathStatus.Note)
	}
	// The one thing this monitor must never do: block traffic for a dead site.
	if a.blocked {
		t.Fatal("protected traffic must not be blocked for an unreachable site")
	}
	switches := 0
	for _, call := range stub.calls {
		if strings.HasPrefix(call, http.MethodPut+" /proxies/SmartVPN") {
			switches++
		}
	}
	// Every node of the region is tried once, including the one it started on:
	// after the others have failed there is nothing else left to try but the
	// first one again.
	if switches != 3 {
		t.Fatalf("the round should try each node of the region once, got %d switches", switches)
	}
}

func TestPathRoundWithoutARegionOnlyReports(t *testing.T) {
	a, stub, _ := pathRoundApp(t, "", "node-a")
	check, _ := checkerThatFailsOnce()
	a.runPathRound(check)

	if !strings.Contains(a.pathStatus.Note, "地区未知") {
		t.Fatalf("note = %q", a.pathStatus.Note)
	}
	for _, call := range stub.calls {
		if strings.HasPrefix(call, http.MethodPut) {
			t.Fatalf("nothing may be switched without a region: %v", stub.calls)
		}
	}
}

func TestPathRoundDoesNothingWhileDisconnected(t *testing.T) {
	a, _, _ := pathRoundApp(t, "JP", "jp-a")
	a.kernel = nil
	called := false
	a.runPathRound(func(_ string, _ []pathTarget) []siteCheckResult {
		called = true
		return nil
	})
	if called {
		t.Fatal("a round must not run without a kernel")
	}
}
