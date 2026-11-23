package main

import (
	"bytes"
	"encoding/binary"

	"path/filepath"
	"strings"
	"sync/atomic"
)

var settingsFlowRecoveries atomic.Uint64

type settingsFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type settingsFlowStart func([]byte) (settingsFlowSession, error)
type settingsFlowPlan struct {
	input                          []byte
	next                           settings
	presence, invalid              uint16
	conflict                       bool
	changed, merged, found, ensure bool
	cacheID                        string
}

func settingsFlowFlag(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}
func settingsFlowEnsure(s settingsServiceState) bool {
	url := strings.TrimSpace(s.Settings.SubscriptionURL)
	if url == "" || url == mergedSubscriptionURL {
		return false
	}
	for _, entry := range s.Subscriptions {
		if entry.URL == url {
			return false
		}
	}
	return true
}
func prepareSettingsFlow(s settingsServiceState, r settingsServiceRequest) settingsFlowPlan {
	p := r.Patch
	f := settingsFlowPlan{next: s.Settings}
	mark := func(bit uint) { f.presence |= 1 << bit }
	invalid := func(bit uint) { f.invalid |= 1 << bit }
	value := func(p *bool) bool { return p != nil && *p }
	if p.ImportPolicy != nil {
		mark(0)
		f.next.ImportPolicy = *p.ImportPolicy
	}
	if p.SocksPort != nil {
		mark(1)
		f.next.SocksPort = *p.SocksPort
		if !validSocksSettingGo(*p.SocksPort, r.Windows) {
			invalid(1)
		}
		f.conflict = *p.SocksPort != 0 && *p.SocksPort == r.APIPort
	}
	if p.GameBypass != nil {
		mark(2)
		f.next.GameBypass = r.GameBypass
		if r.GameError != "" {
			invalid(2)
		}
	}
	if p.StrictPrivacy != nil {
		mark(3)
		f.next.WebRTCProtection = *p.StrictPrivacy
		f.next.DNSProtection = *p.StrictPrivacy
	}
	if p.DomesticWeb != nil {
		mark(4)
		if *p.DomesticWeb && !f.next.DomesticWeb {
			f.next.WebRTCProtection = true
			f.next.DNSProtection = true
		}
		f.next.DomesticWeb = *p.DomesticWeb
	}
	if p.WebRTCProtection != nil {
		mark(5)
		f.next.WebRTCProtection = *p.WebRTCProtection
	}
	if p.DNSProtection != nil {
		mark(6)
		f.next.DNSProtection = *p.DNSProtection
	}
	if p.ConnectionMode != nil {
		mark(7)
		f.next.ConnectionMode = strings.TrimSpace(*p.ConnectionMode)
		if !r.ModeSupported {
			invalid(7)
		}
	}
	if p.WintunPath != nil {
		mark(8)
		f.next.WintunPath = strings.TrimSpace(*p.WintunPath)
		if f.next.WintunPath != "" && !strings.EqualFold(filepath.Ext(f.next.WintunPath), ".dll") {
			invalid(8)
		}
	}
	if p.SubscriptionURL != nil {
		mark(9)
		f.next.SubscriptionURL = strings.TrimSpace(*p.SubscriptionURL)
		if f.next.SubscriptionURL != "" && r.URLValidationError != "" {
			invalid(9)
		}
	}
	if p.MihomoPath != nil {
		mark(10)
		f.next.MihomoPath = strings.TrimSpace(*p.MihomoPath)
		if f.next.MihomoPath != "" && !strings.EqualFold(filepath.Ext(f.next.MihomoPath), ".exe") {
			invalid(10)
		}
	}
	f.next.LockedRegion = s.LockedRegion
	f.next.LockedNode = s.LockedNode
	f.changed = f.next.SubscriptionURL != s.Settings.SubscriptionURL
	f.merged = f.next.SubscriptionURL == mergedSubscriptionURL
	entry, found := settingsServiceCacheEntry(s, f.next.SubscriptionURL)
	f.found = found
	f.cacheID = entry.ID
	copy := s
	copy.Settings = f.next
	f.ensure = settingsFlowEnsure(copy)
	f.input = binary.LittleEndian.AppendUint16(nil, f.presence)
	f.input = binary.LittleEndian.AppendUint16(f.input, f.invalid)
	for _, b := range []bool{f.conflict, s.Settings.DomesticWeb, s.Settings.WebRTCProtection, s.Settings.DNSProtection, value(p.StrictPrivacy), value(p.DomesticWeb), value(p.WebRTCProtection), value(p.DNSProtection), f.changed, f.merged, s.StoreAvailable, f.found, f.ensure} {
		f.input = settingsFlowFlag(f.input, b)
	}
	return f
}
func (f settingsFlowPlan) validation() byte {
	for _, v := range [][2]uint16{{1, 2}, {2, 4}, {7, 5}, {8, 6}, {9, 7}, {10, 8}} {
		if f.presence&(1<<v[0]) != 0 && f.invalid&(1<<v[0]) != 0 {
			return byte(v[1])
		}
		if v[0] == 1 && f.presence&2 != 0 && f.conflict {
			return 3
		}
	}
	return 0
}

// Returns handled=false only before any effect. Once ApplySettings succeeds,
// transport failure consumes the remaining local queue rather than replaying it.
func runRustSettingsServiceWithStart(s *settingsServiceState, r settingsServiceRequest, fx settingsServiceEffects, start settingsFlowStart) (settingsServiceResult, bool) {
	f := prepareSettingsFlow(*s, r)
	session, err := start(f.input)
	if err != nil {
		return settingsServiceResult{}, false
	}
	defer session.close()
	queue := [][]byte{}
	validate := func() {
		if code := f.validation(); code != 0 {
			queue = append(queue, []byte{13, code})
		} else {
			queue = append(queue, []byte{1, 0})
		}
	}
	if f.presence&1 != 0 {
		queue = append(queue, []byte{1, 1})
	} else {
		validate()
	}
	finish := func() {
		if settingsFlowEnsure(*s) {
			queue = append(queue, []byte{11}, []byte{12})
		}
		queue = append(queue, []byte{7, 1})
	}
	remove := func() { queue = append(queue, []byte{10, 0}, []byte{10, 1}, []byte{10, 2}) }
	started, committed, broken := false, false, false
	last := uint64(0)
	stop := func() (settingsServiceResult, bool) {
		settingsFlowRecoveries.Add(1)
		if started {
			return settingsServiceResult{Status: 500, Message: "settings continuation failed"}, true
		}
		return settingsServiceResult{}, false
	}
	for count := 0; count < 64; count++ {
		if len(queue) == 0 {
			return stop()
		}
		expected := queue[0]
		var step uint64
		if !broken {
			raw, n, e := session.next()
			step = n
			if e != nil || step != last+1 || !bytes.Equal(raw, expected) {
				if !committed {
					return stop()
				}
				broken = true
				settingsFlowRecoveries.Add(1)
				fx.Logf("settings continuation failed after commit; finishing remaining local effects without replay")
			}
		}
		if expected[0] == 13 {
			return settingsServiceResultForCode(uint32(expected[1]), r), true
		}
		queue = queue[1:]
		started = true
		reply := []byte{0}
		switch expected[0] {
		case 1:
			running := fx.KernelRunning()
			reply = settingsFlowFlag([]byte{1}, running)
			if running {
				code := byte(9)
				if expected[1] != 0 {
					code = 1
				}
				queue = append(queue, []byte{13, code})
			} else if expected[1] != 0 {
				validate()
			} else {
				b := binary.LittleEndian.AppendUint16([]byte{2}, f.presence)
				for _, v := range []bool{f.next.DomesticWeb, f.next.WebRTCProtection, f.next.DNSProtection} {
					b = settingsFlowFlag(b, v)
				}
				queue = append(queue, b)
			}
		case 2:
			markSettingsControlIntent(s, settingsRequestControlMask(r.Patch, s.Settings))
			s.Settings = f.next
			committed = true
			if f.changed && !f.merged {
				queue = append(queue, []byte{3}, []byte{4})
				if s.StoreAvailable {
					queue = append(queue, []byte{5}, []byte{6})
				}
				queue = append(queue, []byte{7, 0}, []byte{8, 0})
				if f.found {
					queue = append(queue, []byte{9})
				} else {
					remove()
					finish()
				}
			} else {
				finish()
			}
		case 3:
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
		case 4:
			fx.RemoveCachedNodes()
		case 5:
			if err := fx.ClearHealth(); err != nil {
				fx.Logf("could not clear stored node health: %v", err)
			}
		case 6:
			if err := fx.ClearRegions(); err != nil {
				fx.Logf("could not clear stored exit regions: %v", err)
			}
		case 7:
			err := fx.SaveSettings(*s)
			reply = settingsFlowFlag([]byte{1}, err == nil)
			if expected[1] != 0 {
				code := byte(0)
				if err != nil {
					code = 10
				}
				queue = append(queue, []byte{13, code})
			}
		case 8:
			s.ProfileFromCache = expected[1] != 0
			markCatalogFromCache(s, expected[1] != 0)
		case 9:
			err := fx.UseCachedProfile(f.cacheID, s)
			reply = settingsFlowFlag([]byte{1}, err == nil)
			if err == nil {
				queue = append(queue, []byte{8, 1})
			} else {
				remove()
			}
			finish()
		case 10:
			fx.RemoveProfile([]string{"", ".source", ".format"}[expected[1]])
		case 11:
			s.Subscriptions = append(s.Subscriptions, settingsServiceNewEntry(strings.TrimSpace(s.Settings.SubscriptionURL)))
			markCatalogAppend(s, settingsServiceNewEntry(strings.TrimSpace(s.Settings.SubscriptionURL)))
		case 12:
			_ = fx.SaveSubscriptions(*s)
		default:
			return stop()
		}
		if !broken {
			last = step
			if err := session.advance(step, reply); err != nil {
				if !committed {
					return stop()
				}
				broken = true
				settingsFlowRecoveries.Add(1)
				fx.Logf("settings continuation failed after commit; finishing remaining local effects without replay")
			}
		}
	}
	return stop()
}
