package main

import (
	"strings"
	"testing"
	"time"
)

type adaptiveHealthBridge struct {
	appHealthEffects
	run   adaptiveIndependentRunner
	pass  bool
	sites *int
}

func (f adaptiveHealthBridge) ConfirmTraffic() bool {
	q := adaptiveServiceRequest{Op: "confirm"}
	e := newAdaptiveEffects(nil, f.a, q)
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"running": func(adaptiveServiceEffect) adaptiveServiceReply { return adaptiveServiceReply{Flag: true} }, "site": func(v adaptiveServiceEffect) adaptiveServiceReply {
		*f.sites++
		if v.Duration != 5*time.Second {
			panic("confirm cadence changed")
		}
		if f.pass && v.Index == 1 {
			return adaptiveServiceReply{Text: "passed"}
		}
		return adaptiveServiceReply{Text: "error"}
	}}
	r := f.run(q, e)
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Bool
}

type adaptiveAppRunner func(appServiceRequest, appServiceEffects) appServiceResult
type adaptiveHealthRunner func(*healthServiceState, healthServiceRequest, healthServiceEffects)

func adaptiveIndependentHealthComposition(t *testing.T, run adaptiveIndependentRunner, appRun adaptiveAppRunner, healthRun adaptiveHealthRunner) {
	for _, pass := range []bool{true, false} {
		t.Run(map[bool]string{true: "independent-pass", false: "exhausted"}[pass], func(t *testing.T) {
			a, calls, _ := migrationHealthApp(t)
			a.confirmProbe = nil
			a.mixedPort = 7890
			now := time.Unix(12345, 0)
			for _, name := range []string{"active", "standby"} {
				a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
			}
			a.writeRegionRecordLocked("foreign", regionRecord{Name: "foreign", Country: "US", Status: regionVerified, VerifiedAt: now})
			a.writeHealthRecordLocked("foreign", nodeHealth{Name: "foreign", State: healthHealthy, LatencyMs: 1})
			sites := 0
			rounds := a.healthMetaLocked().Params.FailureThreshold
			if pass {
				rounds += 2
			}
			for i := 0; i < rounds; i++ {
				q := appServiceRequest{Op: "once"}
				e := newAppControlEffects(a, q)
				e.hooks = map[string]func(appServiceEffect) appServiceReply{"running": func(appServiceEffect) appServiceReply { return appServiceReply{Flag: true} }, "active-get": func(appServiceEffect) appServiceReply { return appServiceReply{Text: "active"} }, "delays": func(appServiceEffect) appServiceReply { return appServiceReply{Err: adaptiveIndependentErr} }, "log": func(appServiceEffect) appServiceReply { return appServiceReply{} }, "sweep": func(v appServiceEffect) appServiceReply {
					if v.Count != 1 {
						t.Fatal("bulk failure classification")
					}
					s := healthStateFromApp(a)
					healthRun(&s, healthServiceRequest{Kind: "failed", Active: "active", Now: now.Add(time.Duration(i) * time.Minute)}, adaptiveHealthBridge{appHealthEffects{a}, run, pass, &sites})
					applyHealthServiceState(a, s)
					return appServiceReply{}
				}}
				r := appRun(q, e)
				if r.Err != nil {
					t.Fatal(r.Err)
				}
			}
			puts := 0
			for _, c := range *calls {
				if strings.HasPrefix(c, "PUT ") {
					puts++
					if c != `PUT /proxies/SmartVPN {"name":"REJECT"}` {
						t.Fatal("cross-region", c)
					}
				}
			}
			if pass {
				if puts != 0 || a.healthMetaLocked().Blocked || sites != 6 {
					t.Fatalf("passing flow changed put%d sites%d", puts, sites)
				}
			} else if puts != 1 || !a.healthMetaLocked().Blocked || sites != len(confirmProbeTargets) {
				t.Fatalf("exhaustion put%d sites%d", puts, sites)
			}
			if a.controlMetaLocked().RuntimeLockedRegion != "JP" || a.controlMetaLocked().RuntimeLockedNode != "active" {
				t.Fatal("pin changed")
			}
		})
	}
}
func TestAdaptiveIndependentSelectedHealthComposition(t *testing.T) {
	adaptiveIndependentHealthComposition(t, runSelectedAdaptiveService, runSelectedAppService, runSelectedHealthService)
}
func TestAdaptiveIndependentAuthorityError(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	_ = a.service.Close()
	q := adaptiveServiceRequest{Op: "block"}
	e := newAdaptiveEffects(nil, a, q)
	rejects := 0
	e.hooks = map[string]func(adaptiveServiceEffect) adaptiveServiceReply{"reject": func(adaptiveServiceEffect) adaptiveServiceReply { rejects++; return adaptiveServiceReply{} }}
	r := runAdaptiveWithEffects(q, e)
	if r.Err == nil || rejects != 0 {
		t.Fatalf("authority must not turn into failed path %+v rejects%d", r, rejects)
	}
}
