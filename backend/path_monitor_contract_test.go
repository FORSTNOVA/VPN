package main

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func migrationOriginalNormalizePathChecks(input pathCheckSettings) (pathCheckSettings, error) {
	result := pathCheckSettings{Enabled: input.Enabled, IntervalSec: input.IntervalSec}
	if result.IntervalSec == 0 {
		result.IntervalSec = pathCheckDefaultInterval
	}
	if result.IntervalSec < pathCheckMinInterval {
		result.IntervalSec = pathCheckMinInterval
	}
	if result.IntervalSec > pathCheckMaxInterval {
		result.IntervalSec = pathCheckMaxInterval
	}

	seen := map[string]bool{}
	limit := pathCheckMaxTargets - len(pathCheckPresets)
	for _, target := range input.Targets {
		raw := strings.TrimSpace(target.URL)
		if raw == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return pathCheckSettings{}, fmt.Errorf("网址无效：%s（只支持 http/https）", raw)
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		label := strings.TrimSpace(target.Label)
		if label == "" {
			label = parsed.Hostname()
		}
		if len([]rune(label)) > 40 {
			return pathCheckSettings{}, errors.New("名称最多 40 个字")
		}
		if len(result.Targets) >= limit {
			return pathCheckSettings{}, fmt.Errorf("最多 %d 个检测目标（含 %d 个内置项）",
				pathCheckMaxTargets, len(pathCheckPresets))
		}
		result.Targets = append(result.Targets, pathTarget{
			Label: label, URL: raw, Enabled: target.Enabled, Builtin: isPresetURL(raw),
		})
	}
	for _, preset := range pathCheckPresets {
		if seen[preset.URL] {
			continue
		}
		result.Targets = append(result.Targets, preset)
	}
	return result, nil
}

func migrationMonitorNormalizeCases() []pathCheckSettings {
	out := []pathCheckSettings{{}, {Enabled: true, IntervalSec: -5}, {IntervalSec: 5000}, {Targets: []pathTarget{{Label: " renamed ", URL: " https://www.google.com/generate_204 ", Enabled: false}, {URL: "https://synthetic.invalid/a", Enabled: true}, {URL: "https://synthetic.invalid/a", Enabled: false}}}, {Targets: []pathTarget{{URL: "file:///tmp/no"}}}, {Targets: []pathTarget{{URL: "https://synthetic.invalid", Label: strings.Repeat("界", 41)}}}, {Targets: []pathTarget{{URL: "https://synthetic.invalid", Label: strings.Repeat("界", 40)}}}}
	for _, n := range []int{14, 15} {
		v := pathCheckSettings{}
		for i := 0; i < n; i++ {
			v.Targets = append(v.Targets, pathTarget{URL: fmt.Sprintf("https://synthetic%d.invalid", i), Enabled: true})
		}
		out = append(out, v)
	}
	v := pathCheckSettings{Targets: append([]pathTarget(nil), pathCheckPresets...)}
	for i := 0; i < 9; i++ {
		v.Targets = append(v.Targets, pathTarget{URL: fmt.Sprintf("https://extra%d.invalid", i)})
	}
	return append(out, v)
}
func TestMigrationPathMonitorSafeNormalizeLegacy(t *testing.T) {
	for i, in := range migrationMonitorNormalizeCases() {
		a, ae := migrationOriginalNormalizePathChecks(in)
		b, be := normalizePathChecks(in)
		if fmt.Sprint(ae) != fmt.Sprint(be) || !reflect.DeepEqual(a, b) {
			t.Fatalf("case%d legacy=%#v %v got=%#v %v", i, a, ae, b, be)
		}
	}
}

func migrationOriginalPathDemand(snapshot kernelTraffic, targets []pathTarget, previous map[string]pathConnectionSample) []string {
	current := make(map[string]pathConnectionSample, len(snapshot.Connections))
	demanded := make(map[string]bool)
	for _, connection := range snapshot.Connections {
		process := strings.ToLower(connectionProcess(connection.Metadata))
		if strings.Contains(process, "smartvpn") {
			continue
		}
		host := normalizePathHost(connection.Metadata.Host)
		if host == "" {
			continue
		}
		id := connection.ID
		if id == "" {
			id = connection.Start + "|" + host + "|" + string(connection.Metadata.DestinationPort)
		}
		sample := pathConnectionSample{upload: connection.Upload}
		prior, seen := previous[id]
		current[id] = sample
		if seen && connection.Upload <= prior.upload {
			continue
		}
		for _, target := range targets {
			targetURL, parseErr := url.Parse(target.URL)
			if parseErr != nil || !pathHostMatches(host, normalizePathHost(targetURL.Hostname())) {
				continue
			}
			demanded[target.Label] = true
		}
	}
	clear(previous)
	for id, sample := range current {
		previous[id] = sample
	}
	labels := make([]string, 0, len(demanded))
	for label := range demanded {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

type monitorBadPathSession struct{ closes int }

func (*monitorBadPathSession) next() ([]byte, uint64, error) {
	return nil, 0, errors.New("synthetic first frame")
}
func (*monitorBadPathSession) advance(uint64, []byte) error { return nil }
func (s *monitorBadPathSession) close()                     { s.closes++ }
func TestMigrationPathMonitorSafeGuardFallback(t *testing.T) {
	a := &app{}
	token := make(chan struct{})
	a.pathCancel = token
	q := pathServiceRequest{Op: "baseline"}
	bad := &monitorBadPathSession{}
	entered := withPathMonitorRound(a, token, q, func(f *appPathEffects) {
		f.hooks = map[string]func(pathServiceEffect) pathServiceReply{"running": func(pathServiceEffect) pathServiceReply { return pathServiceReply{Bool: false} }}
		_, handled := runRustPathServiceWithStart(q, f, func([]byte) (pathFlowSession, error) { return bad, nil })
		if handled {
			t.Fatal("bad first frame handled")
		}
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("pre-effect fallback released admission lock")
		}
		if !f.initialLock || !f.locked {
			t.Fatal("guard ownership lost")
		}
		runGoPathService(q, f)
	})
	if !entered || bad.closes != 1 {
		t.Fatal("guard/session lifecycle")
	}
	if !a.mu.TryLock() {
		t.Fatal("fallback leaked lock")
	}
	a.mu.Unlock()
}
func TestMigrationPathMonitorSafeGuardSelectorAndPanic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		a := &app{}
		token := make(chan struct{})
		a.pathCancel = token
		q := pathServiceRequest{Op: "baseline"}
		func() {
			defer func() { _ = recover() }()
			withPathMonitorRound(a, token, q, func(f *appPathEffects) {
				f.hooks = map[string]func(pathServiceEffect) pathServiceReply{"running": func(pathServiceEffect) pathServiceReply { return pathServiceReply{Bool: false} }}
				if fail {
					f.hooks["lock"] = func(pathServiceEffect) pathServiceReply { panic("synthetic first effect") }
				}
				runSelectedPathService(q, f)
			})
		}()
		if !a.mu.TryLock() {
			t.Fatal("guard selector/panic leaked lock", fail)
		}
		a.mu.Unlock()
	}
}
