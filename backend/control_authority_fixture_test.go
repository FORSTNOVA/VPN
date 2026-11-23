package main

import (
	"errors"
	"reflect"
	"testing"
)

type controlSettingsFX struct {
	settingsMemoryEffects
	a *app
}

func (f *controlSettingsFX) SaveSettings(s settingsServiceState) error {
	applySettingsServiceState(f.a, s)
	return f.settingsMemoryEffects.SaveSettings(s)
}
func (f *controlSettingsFX) SaveSubscriptions(s settingsServiceState) error {
	applySettingsServiceState(f.a, s)
	return f.settingsMemoryEffects.SaveSubscriptions(s)
}

type controlSettingsRun func(*settingsServiceState, settingsServiceRequest, settingsServiceEffects) settingsServiceResult
type controlChainRun func(chainServiceRequest, chainServiceEffects) chainServiceResult

func controlAuthorityWorkflowGolden(t *testing.T, factory authorityFactory, srun controlSettingsRun, crun controlChainRun) {
	a := regionAuthorityNew(t, factory)
	owner := a.service.backend
	testSetSettings(a, settings{SelectedNode: "selected", SelectionMode: "manual"})
	s := settingsStateFromApp(a)
	fx := &controlSettingsFX{a: a, settingsMemoryEffects: settingsMemoryEffects{SaveFailures: []bool{true}}}
	q := prepareSettingsServiceRequest(settingsPatch{SocksPort: settingsInt(2345)}, true, false, 9999, true)
	r := srun(&s, q, fx)
	applySettingsServiceState(a, s)
	if r.Status != 500 || a.controlLocked().SocksPort != 2345 || len(fx.Saves) != 1 {
		t.Fatal("settings save failure rollback/replay", r)
	}
	// An unrelated control commit during a failed chain save must survive the
	// graph rollback, whose exact old map, backing array and capacity survive.
	h := make([]string, 2, 5)
	copy(h, []string{"a", "b"})
	old := map[string][]string{"old": h, "shared": h}
	a.settings.Chains = old
	a.settings.Chain = proxyChain{Name: "old", Enabled: true, Hops: h}
	a.manualNodes = chainFixtureNodes()
	cq := chainServiceRequest{Op: "save-profile", Name: "new", Original: "old", Hops: []string{"b", "a"}}
	ce := safeChainFX(a, cq)
	saves := 0
	decodes := 0
	ce.decode = func([]byte) ([]map[string]any, error) { decodes++; return nil, nil }
	ce.save = func() error {
		saves++
		a.patchControlLocked(controlSelectedNode|controlRuntimeLockedNode, serviceControlState{SelectedNode: "concurrent", RuntimeLockedNode: "pin"})
		return errors.New("synthetic save")
	}
	cr := crun(cq, ce)
	if cr.Status != 500 || saves != 1 || decodes != 2 || a.settings.Chain.Name != "old" || reflect.ValueOf(a.settings.Chains).UnsafePointer() != reflect.ValueOf(old).UnsafePointer() || &a.settings.Chain.Hops[0] != &h[0] || cap(a.settings.Chain.Hops) != 5 {
		t.Fatal("chain rollback identity/order", cr, saves, decodes)
	}
	h[0] = "outside"
	if a.settings.Chains["shared"][0] != "outside" || a.settings.Chain.Hops[0] != "outside" {
		t.Fatal("chain write-through lost")
	}
	if a.controlLocked().SelectedNode != "concurrent" || a.controlLocked().RuntimeLockedNode != "pin" || a.service.backend != owner {
		t.Fatal("chain rollback overwrote native control")
	}
}
func TestServiceStateSafeControlWorkflow(t *testing.T) {
	controlAuthorityWorkflowGolden(t, newSelectedServiceStateBackend, runSelectedSettingsService, runSelectedChainService)
}
