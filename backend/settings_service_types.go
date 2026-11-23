package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type settingsObservationClear struct{ Applied bool }

// An explicit resource replacement, retaining the requested Go slice verbatim.
type settingsCachedNodesIntent struct {
	Nodes   []proxyNode
	Applied bool
}

func markSettingsCachedNodes(s *settingsServiceState) {
	if s.ControlBaseline != nil {
		s.CachedNodesIntent = &settingsCachedNodesIntent{Nodes: s.CachedNodes}
	}
}

type settingsControlIntent struct {
	Mask    uint64
	Applied bool
}

const settingsClearPinsMask = controlSelectedNode | controlSelectionMode | controlLockedRegion | controlLockedNode | controlRuntimeLockedRegion | controlRuntimeLockedNode

func markSettingsControlIntent(s *settingsServiceState, mask uint64) {
	if s.ControlBaseline == nil {
		return
	} // Pure planners have no application target.
	if s.ControlIntent == nil || s.ControlIntent.Applied {
		s.ControlIntent = &settingsControlIntent{}
	}
	s.ControlIntent.Mask |= mask
}
func settingsRequestControlMask(p settingsPatch, prior settings) uint64 {
	var m uint64
	if p.ImportPolicy != nil {
		m |= controlImportPolicy
	}
	if p.SocksPort != nil {
		m |= controlSocksPort
	}
	if p.GameBypass != nil {
		m |= controlGameBypass
	}
	if p.StrictPrivacy != nil {
		m |= controlWebRTCProtection | controlDNSProtection
	}
	if p.DomesticWeb != nil {
		m |= controlDomesticWeb
		if *p.DomesticWeb && !prior.DomesticWeb {
			m |= controlWebRTCProtection | controlDNSProtection
		}
	}
	if p.WebRTCProtection != nil {
		m |= controlWebRTCProtection
	}
	if p.DNSProtection != nil {
		m |= controlDNSProtection
	}
	if p.ConnectionMode != nil {
		m |= controlConnectionMode
	}
	if p.WintunPath != nil {
		m |= controlWintunPath
	}
	if p.SubscriptionURL != nil {
		m |= controlSubscriptionURL
	}
	if p.MihomoPath != nil {
		m |= controlMihomoPath
	}
	// The original settings transaction explicitly copies current runtime pins.
	return m | controlLockedRegion | controlLockedNode
}

// A request-local checkpoint, not a live control mirror. Copies share consumption
// so later save/catalog effects cannot replay already committed control changes.
type settingsControlBaseline struct {
	Settings                 settings
	LockedRegion, LockedNode string
}

func detachedControlSettings(s settings) settings {
	s.Chain = proxyChain{}
	s.Chains = nil
	if s.GameBypass != nil {
		s.GameBypass = append([]string{}, s.GameBypass...)
	}
	return s
}

type settingsServiceState struct {
	CatalogIntents           *catalogIntentQueue        `json:"-"`
	CachedNodesIntent        *settingsCachedNodesIntent `json:"-"`
	ControlIntent            *settingsControlIntent     `json:"-"`
	ControlBaseline          *settingsControlBaseline   `json:"-"`
	ClearObservations        *settingsObservationClear  `json:"-"`
	Settings                 settings
	LockedRegion, LockedNode string
	Blocked                  bool
	BlockReason              string
	Health                   map[string]nodeHealth
	Regions                  map[string]regionRecord
	CachedNodes              []proxyNode
	ProfileFromCache         bool
	Subscriptions            []subscriptionEntry
	StoreAvailable           bool
}
type settingsServiceRequest struct {
	Patch                         settingsPatch
	Windows, Android              bool
	APIPort                       int
	ModeSupported                 bool
	GameBypass                    []string
	GameError, URLValidationError string
}
type settingsServiceResult struct {
	Status  int
	Message string
}
type settingsServiceEffects interface {
	KernelRunning() bool
	RemoveCachedNodes()
	ClearHealth() error
	ClearRegions() error
	SaveSettings(settingsServiceState) error
	UseCachedProfile(string, *settingsServiceState) error
	RemoveProfile(string)
	SaveSubscriptions(settingsServiceState) error
	Logf(string, ...any)
}

func settingsServiceError(message string) error {
	if message == "" {
		return nil
	}
	return errors.New(message)
}
func prepareSettingsServiceRequest(p settingsPatch, windows, android bool, apiPort int, modeSupported bool) settingsServiceRequest {
	r := settingsServiceRequest{Patch: p, Windows: windows, Android: android, APIPort: apiPort, ModeSupported: modeSupported}
	if p.GameBypass != nil {
		var err error
		r.GameBypass, err = normalizeGameBypassGo(*p.GameBypass, android)
		if err != nil {
			r.GameError = err.Error()
		}
	}
	if p.SubscriptionURL != nil {
		address := strings.TrimSpace(*p.SubscriptionURL)
		if address != "" {
			if err := settingsServiceValidateURL(address); err != nil {
				r.URLValidationError = err.Error()
			}
		}
	}
	return r
}
func settingsServiceCacheEntry(s settingsServiceState, address string) (subscriptionEntry, bool) {
	for _, e := range s.Subscriptions {
		if e.URL == address || e.ID == subscriptionHash(address) {
			return e, true
		}
	}
	return subscriptionEntry{}, false
}
func settingsServiceNewEntry(address string) subscriptionEntry {
	return subscriptionEntry{ID: subscriptionHash(address), URL: address, Label: subscriptionLabel(address), Enabled: true}
}
func settingsStateFromApp(a *app) settingsServiceState {
	h := a.healthDomainLocked()
	c := a.controlLocked()
	s := a.settingsLocked()
	baseline := &settingsControlBaseline{Settings: detachedControlSettings(s), LockedRegion: c.RuntimeLockedRegion, LockedNode: c.RuntimeLockedNode}
	return settingsServiceState{CatalogIntents: &catalogIntentQueue{}, ControlBaseline: baseline, Settings: s, LockedRegion: c.RuntimeLockedRegion, LockedNode: c.RuntimeLockedNode, Blocked: h.Blocked, BlockReason: h.BlockReason, Health: h.Health, Regions: h.Regions, CachedNodes: a.borrowCachedNodesLocked(), ProfileFromCache: a.profileFromCacheLocked(), Subscriptions: a.catalogEntriesLocked(), StoreAvailable: a.store != nil}
}

// The snapshot intentionally excludes path configuration. Ordinary settings and
// subscription workflows must never restore a stale path-domain snapshot.
func applySettingsServiceState(a *app, s settingsServiceState) {
	if intent := s.ClearObservations; intent != nil && !intent.Applied {
		// Consume before committing: an uncertain command must not be replayed.
		intent.Applied = true
		a.clearHealthObservationsLocked()
	}
	if baseline := s.ControlBaseline; baseline != nil {
		before := *baseline
		// Consume before commands: uncertain commit must fail closed, never replay.
		*baseline = settingsControlBaseline{Settings: detachedControlSettings(s.Settings), LockedRegion: s.LockedRegion, LockedNode: s.LockedNode}
		var mask uint64
		if intent := s.ControlIntent; intent != nil && !intent.Applied {
			intent.Applied = true
			mask = intent.Mask
		}
		if before.LockedRegion != s.LockedRegion {
			mask |= controlRuntimeLockedRegion
		}
		if before.LockedNode != s.LockedNode {
			mask |= controlRuntimeLockedNode
		}
		a.applySettingsControlPatchLocked(before.Settings, s.Settings, mask, s.LockedRegion, s.LockedNode)
	}
	// Chain and named-chain graphs are never restored by catalog/settings snapshots.
	if intent := s.CachedNodesIntent; intent != nil && !intent.Applied {
		intent.Applied = true
		a.replaceCachedNodesLocked(intent.Nodes, true)
	}
	a.applyCatalogIntentsLocked(&s)
}

type appSettingsEffects struct{ a *app }

func (e appSettingsEffects) KernelRunning() bool { return e.a.kernelRunningLocked() }
func (e appSettingsEffects) RemoveCachedNodes()  { _ = os.Remove(e.a.cachedNodesPath()) }
func (e appSettingsEffects) ClearHealth() error  { return e.a.store.clearHealth() }
func (e appSettingsEffects) ClearRegions() error { return e.a.store.clearRegions() }
func (e appSettingsEffects) SaveSettings(s settingsServiceState) error {
	applySettingsServiceState(e.a, s)
	return e.a.saveSettings()
}

// Always return the latest state, even if cache activation only partially succeeded.
func (e appSettingsEffects) UseCachedProfile(id string, s *settingsServiceState) error {
	applySettingsServiceState(e.a, *s)
	err := e.a.useCachedProfileLocked(id)
	*s = settingsStateFromApp(e.a)
	return err
}
func (e appSettingsEffects) RemoveProfile(suffix string) { _ = os.Remove(e.a.profilePath() + suffix) }
func (e appSettingsEffects) SaveSubscriptions(s settingsServiceState) error {
	applySettingsServiceState(e.a, s)
	return e.a.saveSubscriptionsLocked()
}
func (e appSettingsEffects) Logf(f string, v ...any) { log.Printf(f, v...) }
func settingsServiceValidateURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("subscription URL must be an HTTP(S) URL")
	}
	if raw == mergedSubscriptionURL {
		return nil
	}
	if isInlineSubscriptionGo(raw) {
		return nil
	}
	remoteURLs, inlineLines := categorizeSubscriptionInputGo(raw)
	if len(remoteURLs) == 0 && len(inlineLines) == 0 {
		return errors.New("subscription URL must be an HTTP(S) URL")
	}
	for _, uStr := range remoteURLs {
		u, err := neturlParse(uStr)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return errors.New("subscription URL must be an HTTP(S) URL")
		}
	}
	return nil
}

// Plan under the app lock. Publish cache and health effects only after the
// settings document and any catalog append have been persisted.
type httpSettingsEffects struct {
	appSettingsEffects
	clearNodes, clearHealth, clearRegions, saveCatalog bool
	cacheID                                            string
	remove                                             []string
}

func (e *httpSettingsEffects) RemoveCachedNodes()  { e.clearNodes = true }
func (e *httpSettingsEffects) ClearHealth() error  { e.clearHealth = true; return nil }
func (e *httpSettingsEffects) ClearRegions() error { e.clearRegions = true; return nil }
func (e *httpSettingsEffects) SaveSubscriptions(settingsServiceState) error {
	e.saveCatalog = true
	return nil
}
func (e *httpSettingsEffects) SaveSettings(settingsServiceState) error { return nil }
func (e *httpSettingsEffects) UseCachedProfile(id string, _ *settingsServiceState) error {
	e.cacheID = id
	return nil
}
func (e *httpSettingsEffects) RemoveProfile(suffix string) { e.remove = append(e.remove, suffix) }
func (e *httpSettingsEffects) commit(s settingsServiceState) error {
	doc := settingsDocument{settings: s.Settings, PathChecks: e.a.pathConfigLocked()}
	tmp, err := e.a.prepareSettingsDocument(doc)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	catalogChanged := e.saveCatalog && e.a.store != nil
	if catalogChanged {
		proposed := subscriptionServiceState{Subscriptions: s.Subscriptions, StoreAvailable: true}
		if r := runSelectedSubscriptionService(&proposed, subscriptionServiceRequest{Op: "save"}, catalogReadEffects{appSubscriptionEffects{e.a}}); r.Err != nil {
			return r.Err
		}
	}
	if err := os.Rename(tmp, filepath.Join(e.a.home, "settings.json")); err != nil {
		if catalogChanged {
			return errors.Join(err, e.a.saveSubscriptionsLocked())
		}
		return err
	}
	applySettingsServiceState(e.a, s)
	if e.clearNodes {
		e.appSettingsEffects.RemoveCachedNodes()
	}
	if e.clearHealth {
		if err := e.appSettingsEffects.ClearHealth(); err != nil {
			e.Logf("could not clear stored node health: %v", err)
		}
	}
	if e.clearRegions {
		if err := e.appSettingsEffects.ClearRegions(); err != nil {
			e.Logf("could not clear stored exit regions: %v", err)
		}
	}
	if e.cacheID != "" {
		if err := e.a.useCachedProfileLocked(e.cacheID); err != nil {
			e.a.setProfileFromCacheLocked(false)
			e.remove = []string{"", ".source", ".format"}
		}
	}
	for _, suffix := range e.remove {
		e.appSettingsEffects.RemoveProfile(suffix)
	}
	e.a.refreshSettingsRedactor()
	return nil
}
