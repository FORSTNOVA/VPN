package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type appChinaEffects struct {
	a                  *app
	q                  chinaServiceRequest
	hooks              map[string]func(chinaServiceEffect) chinaServiceReply
	owned              bool
	probeListPending   bool
	probeConfigPending bool
	refreshOwned       bool
	path               string
	port               int
	options            configOptions
}

func newAppChinaEffects(a *app, q chinaServiceRequest) *appChinaEffects {
	return &appChinaEffects{a: a, q: q, refreshOwned: q.Op == "refresh"}
}
func (e *appChinaEffects) Release() {
	defer func() {
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	}()
	defer func() {
		if e.refreshOwned {
			e.refreshOwned = false
			if !e.owned {
				e.a.mu.Lock()
				e.owned = true
			}
			e.a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: false})
		}
	}()
	if e.probeConfigPending {
		defer e.Call(chinaServiceEffect{Kind: "probe-remove-config"})
	}
	if e.probeListPending {
		e.Call(chinaServiceEffect{Kind: "probe-remove-list"})
	}
}
func (e *appChinaEffects) Call(q chinaServiceEffect) (r chinaServiceReply) {
	if q.Kind == "probe-remove-list" {
		if !e.probeListPending {
			return
		}
		e.probeListPending = false
	}
	if q.Kind == "probe-remove-config" {
		if !e.probeConfigPending {
			return
		}
		e.probeConfigPending = false
	}
	completed := false
	defer func() {
		if !completed {
			return
		}

		if q.Kind == "probe-mkdir" && r.Err == nil {
			e.probeListPending = true
			e.probeConfigPending = true
		}
		if q.Kind == "command" && q.Action == 2 {
			e.refreshOwned = q.Flag
		}
		if q.Kind == "spawn" && r.Err == nil {
			e.refreshOwned = false
		}
	}()
	if f := e.hooks[q.Kind]; f != nil {
		r = f(q)
		completed = true
		return
	}
	a := e.a
	path := e.path
	if path == "" {
		path = chinaIPListPath(a.home)
	}
	switch q.Kind {
	case "lock":
		a.mu.Lock()
		e.owned = true
	case "unlock":
		if e.owned {
			e.owned = false
			a.mu.Unlock()
		}
	case "state":
		r.State = a.chinaStateLocked()
	case "command":
		a.chinaCommandLocked(serviceChinaCommand{Action: q.Action, Status: q.Status, Flag: q.Flag})
	case "stat":
		var v os.FileInfo
		v, r.Err = os.Stat(path)
		if r.Err == nil {
			r.Now = v.ModTime()
		}
	case "read":
		r.Body, r.Err = os.ReadFile(path)
	case "lines":
		r.Names = strings.Split(string(q.Body), "\n")
		for i := range r.Names {
			r.Names[i] = strings.TrimSpace(r.Names[i])
		}
	case "cidr":
		var n *net.IPNet
		_, n, r.Err = net.ParseCIDR(q.Name)
		if r.Err == nil {
			r.Text = n.String()
		}
	case "sources":
		r.Names = append([]string(nil), chinaIPMirrors...)
		r.Ranges = append([]string(nil), chinaIPListFiles...)
	case "fetch-override":
		f := e.q.Fetch
		if f == nil {
			f = a.chinaFetch
		}
		if f != nil {
			r.Flag = true
			r.Body, r.Text, r.Err = f()
		}
	case "fetch":
		r.Body, r.Err = fetchChinaIPFile(q.Name)
	case "since":
		r.Flag = time.Since(q.Time) <= chinaIPListTTL
	case "mkdir":
		r.Err = os.MkdirAll(filepath.Dir(path), 0700)
	case "write-temp":
		r.Err = os.WriteFile(path+".tmp", q.Body, 0600)
	case "rename":
		r.Err = os.Rename(path+".tmp", path)
	case "now":
		r.Now = time.Now()
	case "free-port":
		e.port, r.Err = freeLoopbackPort()
	case "probe-mkdir":
		r.Err = os.MkdirAll(filepath.Join(a.home, "providers"), 0700)
		if r.Err == nil {
			e.probeListPending = true
			e.probeConfigPending = true
		}
	case "probe-list-write":
		r.Err = os.WriteFile(filepath.Join(a.home, "providers", "china-probe.txt"), []byte("1.0.1.0/24\n223.255.252.0/23\n"), 0600)
	case "probe-config-write":
		v := fmt.Sprintf(chinaRuleProbe, e.port, chinaProviderName, filepath.Join("providers", "china-probe.txt"), chinaProviderName)
		r.Err = os.WriteFile(filepath.Join(a.home, "china-probe.yaml"), []byte(v), 0600)
	case "probe-check":
		r.Err = a.checkKernelConfig(filepath.Join(a.home, "china-probe.yaml"))
	case "probe-remove-list":
		_ = os.Remove(filepath.Join(a.home, "providers", "china-probe.txt"))
	case "probe-remove-config":
		e.probeConfigPending = false
		_ = os.Remove(filepath.Join(a.home, "china-probe.yaml"))
	case "spawn":
		go func() {
			r := a.runChinaService(chinaServiceRequest{Op: "refresh", Mode: chinaListMode(q.Index)})
			if r.Err != nil {
				log.Printf("domestic address refresh interrupted: %v", r.Err)
			}
		}()
	case "log":
		switch q.Name {
		case "probe":
			log.Printf("the kernel did not accept the domestic address rules, connecting without them: %s", q.Text)
		case "source":
			log.Printf("domestic address list: %d ranges from %s", q.Status.Entries, q.Status.Source)
		case "unavailable":
			log.Printf("domestic traffic will be carried by the proxy: %s", q.Status.Error)
		}
	case "options-base":
		parsed := a.policyPreview.lookup(e.q.Body)
		e.options = configOptions{MixedPort: a.mixedPort, ControllerPort: a.ctrlPort, ProviderPath: filepath.Join("providers", "subscription.yaml"), ControllerSecret: a.ctrlSecret, Rules: subscriptionRulesParsed(e.q.Body, parsed), EntryDNS: entryDNSParsed(e.q.Body, parsed), TUN: a.connectionModeLocked() != modeSystemProxy, trafficPolicy: a.settingsLocked().trafficPolicy, SubscriptionProvider: e.q.HasSubscription, ManualProvider: a.manualCountLocked() > 0}
		if runtime.GOOS == "windows" {
			e.options.ClashRS = clashRSFilename(strings.TrimSpace(a.controlMetaLocked().MihomoPath))
		}
		r.Options = e.options
		r.ImportPolicy = a.controlMetaLocked().ImportPolicy
	case "options-china":
		e.options.ChinaDirect = q.Flag
	case "options-import":
		e.options.SourceRuleOrder = true
		var p sourcePolicy
		var err error
		if e.q.HasSubscription {
			p, err = a.policyPreview.read(e.q.Body)
		}
		if err != nil {
			r.Err = chinaImportError()
		} else {
			_, e.options.Rules = supportedRules(p)
		}
		r.Options = e.options
	case "options-chain":
		e.options.ChainProxies, r.Err = a.buildChain(a.chainSnapshotLocked().Chain, e.q.Body)
		if r.Err == nil && len(e.options.AppGroups) > 0 {
			e.options.appRoutes, r.Err = buildAppRouting(e.options, e.q.Body, a.manualSnapshotLocked())
		}
		r.Options = e.options
	case "options-domains":
		e.options.ProxyServerDomains = mergeProxyServerDomains(proxyServerDomains(e.q.Body), manualServerDomains(a.manualSnapshotLocked()))
		r.Options = e.options
	default:
		panic("unknown china effect: " + q.Kind)
	}
	completed = true
	return
}
func (a *app) runChinaService(q chinaServiceRequest) (r chinaServiceResult) {
	return runChinaServiceWithEffects(q, newAppChinaEffects(a, q))
}
func runChinaServiceWithEffects(q chinaServiceRequest, e *appChinaEffects) (r chinaServiceResult) {
	defer func() {
		if v := recover(); v != nil {
			r.Err = fmt.Errorf("china service interrupted: %v", v)
		}
	}()
	defer e.Release()
	return runSelectedChinaService(q, e)
}
func (a *app) runChinaOptions(profile []byte, has bool) (configOptions, error) {
	r := a.runChinaService(chinaServiceRequest{Op: "options", Body: profile, HasSubscription: has})
	return r.Options, r.Err
}
func chinaHTTPFailure(w http.ResponseWriter, e error) {
	http.Error(w, "China Direct state unavailable", http.StatusInternalServerError)
}
