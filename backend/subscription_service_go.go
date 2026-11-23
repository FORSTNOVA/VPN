package main

import (
	"fmt"
	"sort"
	"strings"
)

func subscriptionServiceSave(s *subscriptionServiceState, fx subscriptionServiceEffects) error {
	if !s.StoreAvailable {
		return nil
	}
	stored := make([]subscriptionEntry, 0, len(s.Subscriptions))
	for _, e := range s.Subscriptions {
		r := fx.Call("seal-secret", e.URL, s)
		if r.Err != nil {
			return r.Err
		}
		e.URL = r.Text
		stored = append(stored, e)
	}
	copyState := *s
	copyState.Subscriptions = stored
	return fx.Call("write-registry", "", &copyState).Err
}
func subscriptionServiceEnsure(s *subscriptionServiceState, url string, fx subscriptionServiceEffects) {
	url = strings.TrimSpace(url)
	if url == "" || url == mergedSubscriptionURL {
		return
	}
	for _, e := range s.Subscriptions {
		if e.URL == url {
			return
		}
	}
	facts := fx.Call("describe-url", url, s).Entry
	s.Subscriptions = append(s.Subscriptions, subscriptionEntry{ID: facts.ID, URL: url, Label: facts.Label, Enabled: true})
	markCatalogAppend(s, s.Subscriptions[len(s.Subscriptions)-1])
	_ = subscriptionServiceSave(s, fx)
}
func subscriptionServiceClearNode(s *subscriptionServiceState, fx subscriptionServiceEffects) {
	s.Settings.SelectedNode = ""
	s.Settings.SelectionMode = "auto"
	s.Settings.LockedRegion = ""
	s.Settings.LockedNode = ""
	s.LockedRegion = ""
	s.LockedNode = ""
	markSettingsControlIntent(s, settingsClearPinsMask)
	s.ClearObservations = &settingsObservationClear{}
	s.Blocked = false
	s.BlockReason = ""
	s.Health = map[string]nodeHealth{}
	s.Regions = map[string]regionRecord{}
	s.CachedNodes = nil
	markSettingsCachedNodes(s)
	fx.Call("remove-nodes", "", s)
	if s.StoreAvailable {
		if r := fx.Call("clear-health", "", s); r.Err != nil {
			fx.Call("log", fmt.Sprintf("could not clear stored node health: %v", r.Err), s)
		}
		if r := fx.Call("clear-regions", "", s); r.Err != nil {
			fx.Call("log", fmt.Sprintf("could not clear stored exit regions: %v", r.Err), s)
		}
	}
	fx.Call("save-settings", "", s)
}
func subscriptionServiceRecord(s *subscriptionServiceState, r subscriptionServiceRequest, fx subscriptionServiceEffects) {
	for i := range s.Subscriptions {
		if s.Subscriptions[i].ID != r.ID {
			continue
		}
		if r.Op == "record-use" {
			s.Subscriptions[i].UpdatedAt = fx.Call("now", "", s).Timestamp
			s.Subscriptions[i].NodeCount = r.NodeCount
		}
		s.Subscriptions[i].LastError = r.Failure
		if r.Op == "record-use" && r.Info != nil {
			s.Subscriptions[i].UserInfo = r.Info
			s.Subscriptions[i].QuotaUpdatedAt = fx.Call("now", "", s).Timestamp
			s.Subscriptions[i].QuotaError = ""
		}
		mask := uint64(catalogLastError)
		if r.Op == "record-use" {
			mask |= catalogUpdatedAt | catalogNodeCount
			if r.Info != nil {
				mask |= catalogInfoHandle | catalogQuotaUpdatedAt | catalogQuotaError
			}
		}
		markCatalogIntent(s, catalogIntent{Action: catalogPatch, Index: i, Mask: mask, Entry: s.Subscriptions[i]})
	}
	_ = subscriptionServiceSave(s, fx)
}
func runGoSubscriptionService(s *subscriptionServiceState, r subscriptionServiceRequest, fx subscriptionServiceEffects) subscriptionServiceResult {
	ok := subscriptionServiceResult{Status: 200, Data: map[string]any{"ok": true}}
	switch r.Op {
	case "save":
		ok.Err = subscriptionServiceSave(s, fx)
		return ok
	case "ensure":
		subscriptionServiceEnsure(s, r.URL, fx)
		return ok
	case "load":
		s.Subscriptions = nil
		markCatalogIntent(s, catalogIntent{Action: catalogReset})
		if s.StoreAvailable {
			loaded := fx.Call("read-registry", "", s).Loaded
			for _, v := range loaded {
				e := v.Entry
				if e.URL == "" {
					continue
				}
				plain := fx.Call("open-secret", e.URL, s)
				if plain.Err != nil {
					fx.Call("log", fmt.Sprintf("could not read a stored subscription URL, dropping that entry: %v", plain.Err), s)
					continue
				}
				e.URL = plain.Text
				if e.ID == "" || e.Label == "" {
					facts := fx.Call("describe-url", e.URL, s).Entry
					if e.ID == "" {
						e.ID = facts.ID
					}
					if e.Label == "" {
						e.Label = facts.Label
					}
				}
				if !v.HasEnabled {
					e.Enabled = true
				}
				s.Subscriptions = append(s.Subscriptions, e)
				markCatalogAppend(s, e)
			}
		}
		subscriptionServiceEnsure(s, s.Settings.SubscriptionURL, fx)
		fx.Call("start-loop", "", s)
		return ok
	case "find", "find-url":
		for _, e := range s.Subscriptions {
			if (r.Op == "find" && e.ID == r.ID) || (r.Op == "find-url" && (e.URL == r.URL || e.ID == r.URLHash)) {
				ok.Entry = e
				ok.Found = true
				return ok
			}
		}
		return ok
	case "list":
		entries := append([]subscriptionEntry(nil), s.Subscriptions...)
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].URL == s.Settings.SubscriptionURL {
				return true
			}
			if entries[j].URL == s.Settings.SubscriptionURL {
				return false
			}
			return entries[i].Label < entries[j].Label
		})
		running := fx.Call("running", "", s).Bool
		ok.Entries = entries
		ok.Data = map[string]any{"subscriptions": entries, "activeUrl": s.Settings.SubscriptionURL, "isMerged": s.Settings.SubscriptionURL == mergedSubscriptionURL, "fromCache": s.ProfileFromCache, "running": running, "autoMergeHours": s.Settings.AutoMergeHours}
		return ok
	case "add":
		if r.URL == "" {
			return subscriptionServiceFailure(400, "empty-url")
		}
		if r.URLValidationError != "" {
			return subscriptionServiceResult{Status: 400, Message: r.URLValidationError}
		}
		for _, e := range s.Subscriptions {
			if e.ID == r.URLHash || e.URL == r.URL {
				return subscriptionServiceFailure(409, "duplicate")
			}
		}
		label := r.Label
		if label == "" {
			label = r.DefaultLabel
		}
		e := subscriptionEntry{ID: r.URLHash, URL: r.URL, Label: label, Prefix: r.Prefix, Enabled: r.HasEnabled && r.Enabled}
		s.Subscriptions = append(s.Subscriptions, e)
		markCatalogAppend(s, e)
		if subscriptionServiceSave(s, fx) != nil {
			s.Subscriptions = s.Subscriptions[:len(s.Subscriptions)-1]
			markCatalogIntent(s, catalogIntent{Action: catalogTruncate})
			return subscriptionServiceFailure(500, "add-save")
		}
		ok.Entry = e
		ok.Data["subscription"] = e
		return ok
	case "update":
		if r.ID == "" {
			return subscriptionServiceFailure(400, "missing-id")
		}
		found := false
		var updated subscriptionEntry
		for i := range s.Subscriptions {
			if s.Subscriptions[i].ID != r.ID {
				continue
			}
			if r.Label != "" {
				s.Subscriptions[i].Label = r.Label
			}
			if r.HasPrefix {
				s.Subscriptions[i].Prefix = r.Prefix
			}
			if r.HasEnabled {
				s.Subscriptions[i].Enabled = r.Enabled
			}
			if r.URL != "" && r.URLValidationError == "" {
				s.Subscriptions[i].URL = r.URL
			}
			mask := uint64(0)
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
			markCatalogIntent(s, catalogIntent{Action: catalogPatch, Index: i, Mask: mask, Entry: s.Subscriptions[i]})
			updated = s.Subscriptions[i]
			found = true
			break
		}
		if !found {
			return subscriptionServiceFailure(404, "not-found")
		}
		if subscriptionServiceSave(s, fx) != nil {
			return subscriptionServiceFailure(500, "save")
		}
		ok.Entry = updated
		ok.Data["subscription"] = updated
		return ok
	case "rename":
		if r.Label == "" || r.LabelRunes > 40 {
			return subscriptionServiceFailure(400, "label")
		}
		found := false
		for i := range s.Subscriptions {
			if s.Subscriptions[i].ID == r.ID {
				s.Subscriptions[i].Label = r.Label
				markCatalogIntent(s, catalogIntent{Action: catalogPatch, Index: i, Mask: catalogLabel, Entry: s.Subscriptions[i]})
				found = true
			}
		}
		if !found {
			return subscriptionServiceFailure(404, "not-found")
		}
		if subscriptionServiceSave(s, fx) != nil {
			return subscriptionServiceFailure(500, "save")
		}
		return ok
	case "record-use", "record-error":
		subscriptionServiceRecord(s, r, fx)
		return ok
	case "hours":
		if r.Hours < 0 || r.Hours > 720 {
			return subscriptionServiceFailure(400, "hours")
		}
		s.Settings.AutoMergeHours = r.Hours
		markSettingsControlIntent(s, controlAutoMergeHours)
		if fx.Call("save-settings", "", s).Err != nil {
			return subscriptionServiceFailure(500, "settings-save")
		}
		ok.Data["autoMergeHours"] = s.Settings.AutoMergeHours
		return ok
	case "delete":
		if r.ID == "" {
			return subscriptionServiceFailure(400, "missing-id")
		}
		if fx.Call("running", "", s).Bool {
			return subscriptionServiceFailure(409, "delete-running")
		}
		var entry subscriptionEntry
		found := false
		for _, e := range s.Subscriptions {
			if e.ID == r.ID {
				entry = e
				found = true
				break
			}
		}
		if !found {
			return subscriptionServiceFailure(404, "not-found")
		}
		previous := append([]subscriptionEntry(nil), s.Subscriptions...)
		markCatalogIntent(s, catalogIntent{Action: catalogBeginDelete})
		kept := make([]subscriptionEntry, 0, len(s.Subscriptions))
		for _, e := range s.Subscriptions {
			if e.ID != r.ID {
				kept = append(kept, e)
			}
		}
		s.Subscriptions = kept
		markCatalogIntent(s, catalogIntent{Action: catalogRemoveID, ID: r.ID})
		clear := false
		if s.Settings.SubscriptionURL == mergedSubscriptionURL {
			enabled := false
			for _, e := range s.Subscriptions {
				if e.Enabled {
					enabled = true
					break
				}
			}
			if enabled {
				if out := fx.Call("merge-caches", "", s); out.Err != nil {
					s.Subscriptions = previous
					markCatalogIntent(s, catalogIntent{Action: catalogRestoreDelete})
					_ = subscriptionServiceSave(s, fx)
					markCatalogIntent(s, catalogIntent{Action: catalogFinishDelete})
					return subscriptionServiceResult{Status: 502, Message: subscriptionServiceMessages["merge-failed"] + out.Err.Error()}
				}
			} else {
				clear = true
			}
		} else if entry.URL == s.Settings.SubscriptionURL {
			clear = true
		}
		if clear {
			s.Settings.SubscriptionURL = ""
			markSettingsControlIntent(s, controlSubscriptionURL)
			s.ProfileFromCache = false
			markCatalogFromCache(s, false)
			subscriptionServiceClearNode(s, fx)
			fx.Call("remove-config", "", s)
			for _, suffix := range []string{"", ".source", ".format"} {
				fx.Call("remove-profile", suffix, s)
			}
		}
		if subscriptionServiceSave(s, fx) != nil {
			s.Subscriptions = previous
			markCatalogIntent(s, catalogIntent{Action: catalogRestoreDelete})
			_ = subscriptionServiceSave(s, fx)
			markCatalogIntent(s, catalogIntent{Action: catalogFinishDelete})
			return subscriptionServiceFailure(500, "save")
		}
		markCatalogIntent(s, catalogIntent{Action: catalogFinishDelete})
		fx.Call("remove-cache", r.ID, s)
		fx.Call("remove-cache-format", r.ID, s)
		ok.Data["activeUrl"] = s.Settings.SubscriptionURL
		return ok
	case "activate":
		if fx.Call("running", "", s).Bool {
			return subscriptionServiceFailure(409, "activate-running")
		}
		if r.ID == mergedSubscriptionURL {
			out := fx.Call("merge", "", s)
			if out.Err != nil {
				return subscriptionServiceResult{Status: 502, Message: out.Err.Error()}
			}
			ok.Data = subscriptionMergedResult(out.Count)
			return ok
		}
		var entry subscriptionEntry
		found := false
		for _, e := range s.Subscriptions {
			if e.ID == r.ID {
				entry = e
				found = true
				break
			}
		}
		if !found {
			return subscriptionServiceFailure(404, "not-found")
		}
		if entry.URL == s.Settings.SubscriptionURL && fx.Call("profile-matches", "", s).Bool {
			ok.Data["fromCache"] = s.ProfileFromCache
			return ok
		}
		s.Settings.SubscriptionURL = entry.URL
		markSettingsControlIntent(s, controlSubscriptionURL)
		subscriptionServiceClearNode(s, fx)
		s.ProfileFromCache = false
		markCatalogFromCache(s, false)
		fetched := fx.Call("fetch", "", s)
		fromCache := s.ProfileFromCache
		if fetched.Err != nil {
			if fx.Call("use-cache", entry.ID, s).Err != nil {
				subscriptionServiceRecord(s, subscriptionServiceRequest{Op: "record-error", ID: entry.ID, Failure: fetched.Err.Error()}, fx)
				return subscriptionServiceResult{Status: 502, Message: fetched.Err.Error()}
			}
			fromCache = true
			s.ProfileFromCache = true
			markCatalogFromCache(s, true)
		}
		count := fetched.Count
		if fromCache {
			count = len(s.CachedNodes)
		}
		subscriptionServiceRecord(s, subscriptionServiceRequest{Op: "record-use", ID: entry.ID, NodeCount: count}, fx)
		ok.Data = map[string]any{"ok": true, "fromCache": fromCache, "nodes": count, "label": entry.Label, "subscriptionUrl": entry.URL}
		return ok
	}
	panic("unknown subscription service operation: " + r.Op)
}

func subscriptionMergedResult(count int) map[string]any {
	return map[string]any{"ok": true, "fromCache": false, "nodes": count, "label": "多订阅合并", "subscriptionUrl": mergedSubscriptionURL}
}
