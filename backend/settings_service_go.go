package main

import (
	"net/http"
	"path/filepath"
	"strings"
)

// Pure use-case oracle: validation order, state transitions and ordered effects.
func runGoSettingsService(state *settingsServiceState, request settingsServiceRequest, effects settingsServiceEffects) settingsServiceResult {
	patch := request.Patch
	next := state.Settings
	if patch.ImportPolicy != nil {
		if effects.KernelRunning() {
			return settingsServiceResult{Status: 409, Message: "请先断开连接再更改订阅分流"}
		}
		next.ImportPolicy = *patch.ImportPolicy
	}
	if patch.SocksPort != nil {
		if !validSocksSettingGo(*patch.SocksPort, request.Windows) {
			return settingsServiceResult{Status: http.StatusBadRequest, Message: "Windows SOCKS5 端口须为 1–65535，或设为 0 自动分配"}
		}
		next.SocksPort = *patch.SocksPort
		if next.SocksPort != 0 && next.SocksPort == request.APIPort {
			return settingsServiceResult{Status: http.StatusConflict, Message: "端口已被本地管理服务使用，请选择其他端口"}
		}
	}
	if patch.GameBypass != nil {
		names, err := request.GameBypass, settingsServiceError(request.GameError)
		if err != nil {
			return settingsServiceResult{Status: http.StatusBadRequest, Message: err.Error()}
		}
		next.GameBypass = names
	}
	if patch.StrictPrivacy != nil {
		next.WebRTCProtection, next.DNSProtection = *patch.StrictPrivacy, *patch.StrictPrivacy
	}
	if patch.DomesticWeb != nil {
		if *patch.DomesticWeb && !next.DomesticWeb {
			next.WebRTCProtection, next.DNSProtection = true, true
		}
		next.DomesticWeb = *patch.DomesticWeb
	}
	if patch.WebRTCProtection != nil {
		next.WebRTCProtection = *patch.WebRTCProtection
	}
	if patch.DNSProtection != nil {
		next.DNSProtection = *patch.DNSProtection
	}
	if patch.ConnectionMode != nil {
		mode := strings.TrimSpace(*patch.ConnectionMode)
		// Which modes exist is the platform's answer, not this route's: the
		// window offers what this platform has, and a mode from the other one is
		// refused rather than stored and quietly ignored.
		if !request.ModeSupported {
			return settingsServiceResult{Status: http.StatusBadRequest, Message: "connection mode must be one this platform offers"}
		}
		next.ConnectionMode = mode
	}
	if patch.WintunPath != nil {
		path := strings.TrimSpace(*patch.WintunPath)
		if path != "" && !strings.EqualFold(filepath.Ext(path), ".dll") {
			return settingsServiceResult{Status: http.StatusBadRequest, Message: "wintun path must point to a .dll file"}
		}
		next.WintunPath = path
	}
	if patch.SubscriptionURL != nil {
		address := strings.TrimSpace(*patch.SubscriptionURL)
		if address != "" {
			if err := settingsServiceError(request.URLValidationError); err != nil {
				return settingsServiceResult{Status: http.StatusBadRequest, Message: err.Error()}
			}
		}
		next.SubscriptionURL = address
	}
	if patch.MihomoPath != nil {
		path := strings.TrimSpace(*patch.MihomoPath)
		if path != "" && !strings.EqualFold(filepath.Ext(path), ".exe") {
			return settingsServiceResult{Status: http.StatusBadRequest, Message: "Mihomo path must point to an .exe file"}
		}
		next.MihomoPath = path
	}

	if effects.KernelRunning() {
		return settingsServiceResult{Status: http.StatusConflict, Message: "disconnect before changing settings"}
	}
	// The lock is in-memory state with a route of its own: keep what it holds now
	// rather than what the stored settings happen to say.
	next.LockedRegion = state.LockedRegion
	next.LockedNode = state.LockedNode
	urlChanged := next.SubscriptionURL != state.Settings.SubscriptionURL
	markSettingsControlIntent(state, settingsRequestControlMask(patch, state.Settings))
	state.Settings = next
	if urlChanged && next.SubscriptionURL != mergedSubscriptionURL {
		state.Settings.SelectedNode = ""
		state.Settings.SelectionMode = "auto"
		state.Settings.LockedRegion = ""
		state.Settings.LockedNode = ""
		state.LockedRegion = ""
		state.LockedNode = ""
		markSettingsControlIntent(state, settingsClearPinsMask)
		state.ClearObservations = &settingsObservationClear{}
		state.Blocked = false
		state.BlockReason = ""
		state.Health = map[string]nodeHealth{}
		state.Regions = map[string]regionRecord{}
		state.CachedNodes = nil
		markSettingsCachedNodes(state)
		effects.RemoveCachedNodes()
		if state.StoreAvailable {
			if err := effects.ClearHealth(); err != nil {
				effects.Logf("could not clear stored node health: %v", err)
			}
			if err := effects.ClearRegions(); err != nil {
				effects.Logf("could not clear stored exit regions: %v", err)
			}
		}
		_ = effects.SaveSettings(*state)
		state.ProfileFromCache = false
		markCatalogFromCache(state, false)
		if entry, ok := settingsServiceCacheEntry(*state, next.SubscriptionURL); ok && effects.UseCachedProfile(entry.ID, state) == nil {
			state.ProfileFromCache = true
			markCatalogFromCache(state, true)
		} else {
			effects.RemoveProfile("")
			effects.RemoveProfile(".source")
			effects.RemoveProfile(".format")
		}
	}
	address := strings.TrimSpace(state.Settings.SubscriptionURL)
	if address != "" && address != mergedSubscriptionURL {
		found := false
		for _, entry := range state.Subscriptions {
			if entry.URL == address {
				found = true
				break
			}
		}
		if !found {
			state.Subscriptions = append(state.Subscriptions, settingsServiceNewEntry(address))
			markCatalogAppend(state, settingsServiceNewEntry(address))
			_ = effects.SaveSubscriptions(*state)
		}
	}
	if err := effects.SaveSettings(*state); err != nil {
		return settingsServiceResult{Status: 500, Message: "could not save settings"}
	}
	return settingsServiceResult{Status: 200}
}

func settingsServiceResultForCode(code uint32, request settingsServiceRequest) settingsServiceResult {
	switch code {
	case 0:
		return settingsServiceResult{Status: 200}
	case 1:
		return settingsServiceResult{Status: 409, Message: "请先断开连接再更改订阅分流"}
	case 2:
		return settingsServiceResult{Status: http.StatusBadRequest, Message: "Windows SOCKS5 端口须为 1–65535，或设为 0 自动分配"}
	case 3:
		return settingsServiceResult{Status: http.StatusConflict, Message: "端口已被本地管理服务使用，请选择其他端口"}
	case 4:
		return settingsServiceResult{Status: http.StatusBadRequest, Message: request.GameError}
	case 5:
		return settingsServiceResult{Status: http.StatusBadRequest, Message: "connection mode must be one this platform offers"}
	case 6:
		return settingsServiceResult{Status: http.StatusBadRequest, Message: "wintun path must point to a .dll file"}
	case 7:
		return settingsServiceResult{Status: http.StatusBadRequest, Message: request.URLValidationError}
	case 8:
		return settingsServiceResult{Status: http.StatusBadRequest, Message: "Mihomo path must point to an .exe file"}
	case 9:
		return settingsServiceResult{Status: http.StatusConflict, Message: "disconnect before changing settings"}
	case 10:
		return settingsServiceResult{Status: 500, Message: "could not save settings"}
	default:
		return settingsServiceResult{Status: 500, Message: "invalid settings service result"}
	}
}
