package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type subscriptionServiceState = settingsServiceState
type subscriptionServiceRequest struct {
	Op, ID, URL, Label, Prefix                string
	HasPrefix, HasEnabled, Enabled            bool
	Hours, NodeCount                          int
	Failure                                   string
	Info                                      *subscriptionUserInfo
	URLValidationError, DefaultLabel, URLHash string
	LabelRunes                                int
}
type subscriptionLoadedEntry struct {
	Entry      subscriptionEntry
	HasEnabled bool
}
type subscriptionEffectReply struct {
	Err       error
	Bool      bool
	Count     int
	Text      string
	Entry     subscriptionEntry
	Loaded    []subscriptionLoadedEntry
	Timestamp int64
}
type subscriptionServiceResult struct {
	Status  int
	Message string
	Entry   subscriptionEntry
	Entries []subscriptionEntry
	Found   bool
	Data    map[string]any
	Err     error
}
type subscriptionServiceEffects interface {
	Call(string, string, *subscriptionServiceState) subscriptionEffectReply
}

func prepareSubscriptionServiceRequest(r subscriptionServiceRequest) subscriptionServiceRequest {
	// Normalization is transport compatibility, never an early validation return.
	switch r.Op {
	case "add", "ensure", "update":
		r.URL = strings.TrimSpace(r.URL)
	}
	switch r.Op {
	case "update", "delete":
		r.ID = strings.TrimSpace(r.ID)
	}
	switch r.Op {
	case "add", "update", "rename":
		r.Label = strings.TrimSpace(r.Label)
		r.Prefix = strings.TrimSpace(r.Prefix)
	}
	r.LabelRunes = utf8.RuneCountInString(r.Label)
	if r.Op == "add" || r.Op == "find-url" {
		r.URLHash = subscriptionHash(r.URL)
	}
	if (r.Op == "add" || r.Op == "update") && r.URL != "" {
		if e := settingsServiceValidateURL(r.URL); e != nil {
			r.URLValidationError = e.Error()
		}
	}
	if r.Op == "add" {
		r.DefaultLabel = subscriptionLabel(r.URL)
	}
	return r
}
func (a *app) runSubscriptionService(r subscriptionServiceRequest) (out subscriptionServiceResult) {
	defer func() {
		if recover() != nil {
			out = subscriptionServiceResult{Status: 500, Message: "subscription operation interrupted", Err: errors.New("subscription operation interrupted")}
		}
	}()
	r = prepareSubscriptionServiceRequest(r)
	if r.Op == "update" {
		return a.updateCatalogConfigurationLocked(r)
	}
	if r.Op == "list" || r.Op == "save" {
		s := subscriptionServiceState{Subscriptions: a.projectCatalogEntriesLocked(true), StoreAvailable: a.store != nil}
		if r.Op == "list" {
			s.Settings = a.settingsLocked()
			s.ProfileFromCache = a.profileFromCacheLocked()
		}
		return runSelectedSubscriptionService(&s, r, catalogReadEffects{appSubscriptionEffects{a}})
	}
	if r.Op == "activate" && r.ID != mergedSubscriptionURL {
		fx := &activationEffects{appSubscriptionEffects: appSubscriptionEffects{a}}
		s := settingsStateFromApp(a)
		committed := false
		defer func() {
			if err := fx.finish(committed); err != nil {
				out = subscriptionServiceFailure(500, "settings-save")
			}
		}()
		result := runSelectedSubscriptionService(&s, r, fx)
		if fx.err == nil {
			applySettingsServiceState(a, s)
		}
		committed = result.Status == 200
		return result
	}
	s := settingsStateFromApp(a)
	var previousLabels map[int]string
	if r.Op == "rename" {
		previousLabels = map[int]string{}
		for i, e := range s.Subscriptions {
			if e.ID == r.ID {
				previousLabels[i] = e.Label
			}
		}
	}
	hours := s.Settings.AutoMergeHours
	result := runSelectedSubscriptionService(&s, r, appSubscriptionEffects{a})
	applySettingsServiceState(a, s)
	if result.Status >= 500 {
		for i, label := range previousLabels {
			a.patchCatalogEntryLocked(i, catalogLabel, subscriptionEntry{Label: label})
		}
		if r.Op == "hours" {
			a.patchControlLocked(controlAutoMergeHours, serviceControlState{AutoMergeHours: hours})
		}
	}
	return result
}

// The catalog resolves duplicate IDs in registry order. Patch only the supplied
// fields so a settings edit cannot replace a quota resource or its timestamps.
func (a *app) updateCatalogConfigurationLocked(r subscriptionServiceRequest) subscriptionServiceResult {
	if r.ID == "" {
		return subscriptionServiceFailure(400, "missing-id")
	}
	found := a.catalogQueryLocked(serviceCatalogQuery{Kind: 3, ID: r.ID})
	if !found.Found {
		return subscriptionServiceFailure(404, "not-found")
	}
	var mask uint64
	if r.Label != "" {
		mask |= catalogLabel
	}
	if r.HasPrefix {
		mask |= catalogPrefix
	}
	if r.HasEnabled {
		mask |= catalogEnabled
	}
	if r.URL != "" && r.URLValidationError == "" {
		mask |= catalogURL
	}
	if mask != 0 {
		entry := serviceCatalogEntry{Label: r.Label, Prefix: r.Prefix, Enabled: r.Enabled, URL: r.URL}
		a.catalogCommandLocked(serviceCatalogCommand{Action: catalogPatch, Index: found.Count, Mask: mask, Entry: entry})
	}
	if err := a.saveSubscriptionsLocked(); err != nil {
		if mask != 0 {
			a.catalogCommandLocked(serviceCatalogCommand{Action: catalogPatch, Index: found.Count, Mask: mask, Entry: found.Entry})
		}
		return subscriptionServiceFailure(500, "save")
	}
	row := a.catalogQueryLocked(serviceCatalogQuery{Kind: 1, Index: found.Count})
	entry := a.serviceStateLocked().catalogProjection(row.Entry, true)
	return subscriptionServiceResult{Status: 200, Entry: entry, Data: map[string]any{"ok": true, "subscription": entry}}
}

type catalogReadEffects struct{ appSubscriptionEffects }

func (e catalogReadEffects) Call(kind, arg string, s *subscriptionServiceState) subscriptionEffectReply {
	if kind == "running" {
		return subscriptionEffectReply{Bool: e.a.kernelRunningLocked()}
	}
	return e.appSubscriptionEffects.Call(kind, arg, s)
}

type appSubscriptionEffects struct{ a *app }

func (e appSubscriptionEffects) Call(kind, arg string, s *subscriptionServiceState) (r subscriptionEffectReply) {
	e.a.flushCatalogElementIntentsLocked(s)
	// External composites must expose their complete modeled partial state on BOTH outcomes.
	switch kind {
	case "read-registry":
		if e.a.store == nil {
			return
		}
		body, found, err := e.a.store.getValue(subscriptionsKey)
		if err != nil || !found {
			return
		}
		var entries []subscriptionEntry
		if json.Unmarshal([]byte(body), &entries) != nil {
			log.Printf("stored subscription list was unreadable and is ignored")
			return
		}
		var raw []map[string]any
		_ = json.Unmarshal([]byte(body), &raw)
		for i, en := range entries {
			has := false
			if i < len(raw) {
				_, has = raw[i]["enabled"]
			}
			r.Loaded = append(r.Loaded, subscriptionLoadedEntry{en, has})
		}
	case "open-secret":
		r.Text, r.Err = openSecret(arg)
	case "seal-secret":
		r.Text, r.Err = sealSecret(arg)
	case "describe-url":
		r.Entry = subscriptionEntry{ID: subscriptionHash(arg), Label: subscriptionLabel(arg)}
	case "write-registry":
		body, err := json.Marshal(s.Subscriptions)
		if err != nil {
			r.Err = err
			return
		}
		r.Err = e.a.store.setValue(subscriptionsKey, string(body))
	case "log":
		log.Print(arg)
	case "now":
		r.Timestamp = time.Now().Unix()
	default:
		applySettingsServiceState(e.a, *s)
		defer func() { *s = settingsStateFromApp(e.a) }()
		switch kind {
		case "start-loop":
			e.a.startAutoMergeLoopLocked()
		case "running":
			r.Bool = e.a.kernelRunningLocked()
		case "profile-matches":
			r.Bool = e.a.profileMatchesSubscription()
		case "save-settings":
			r.Err = e.a.saveSettings()
		case "remove-nodes":
			_ = os.Remove(e.a.cachedNodesPath())
		case "clear-health":
			r.Err = e.a.store.clearHealth()
		case "clear-regions":
			r.Err = e.a.store.clearRegions()
		case "remove-config":
			e.a.removeConfigLocked()
		case "remove-profile":
			_ = os.Remove(e.a.profilePath() + arg)
		case "remove-cache":
			_ = os.Remove(e.a.profileCachePath(arg))
		case "remove-cache-format":
			_ = os.Remove(e.a.profileCachePath(arg) + ".format")
		case "use-cache":
			r.Err = e.a.useCachedProfileLocked(arg)
		case "fetch":
			var nodes []proxyNode
			nodes, r.Err = e.a.fetchProfileLocked()
			r.Count = len(nodes)
		case "merge":
			var nodes []proxyNode
			nodes, r.Err = e.a.mergeAllEnabledSubscriptionsLocked()
			r.Count = len(nodes)
		case "merge-caches":
			var nodes []proxyNode
			nodes, r.Err = e.a.mergeEnabledSubscriptionCachesLocked()
			r.Count = len(nodes)
		default:
			panic("unknown subscription effect: " + kind)
		}
	}
	return
}

var subscriptionServiceMessages = map[string]string{
	"empty-url":        "订阅地址不能为空",
	"duplicate":        "该订阅已存在",
	"add-save":         "保存订阅列表失败",
	"missing-id":       "missing subscription id",
	"not-found":        "没有这个订阅",
	"save":             "无法保存订阅列表",
	"label":            "名称需要 1 到 40 个字",
	"delete-running":   "断开连接后再删除订阅",
	"merge-failed":     "无法更新合并节点列表：",
	"activate-running": "断开连接后再切换订阅",
	"hours":            "hours must be between 0 and 720",
	"settings-save":    "could not save settings",
}

func subscriptionServiceFailure(status int, key string) subscriptionServiceResult {
	return subscriptionServiceResult{Status: status, Message: subscriptionServiceMessages[key]}
}

func writeSubscriptionServiceResult(w http.ResponseWriter, r subscriptionServiceResult) {
	if r.Status != 200 {
		http.Error(w, r.Message, r.Status)
		return
	}
	writeJSON(w, r.Data)
}
