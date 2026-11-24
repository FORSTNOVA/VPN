package main

import (
	"errors"
	"reflect"
)

// Control values only. Chain and named-profile resource graphs remain in Go.
type serviceControlState struct {
	AppGroups           appGroups `json:"appGroups"`
	ImportPolicy        bool
	GameBypass          []string
	DomesticWeb         bool
	WebRTCProtection    bool
	DNSProtection       bool
	SubscriptionURL     string
	MihomoPath          string
	SelectedNode        string
	SelectionMode       string
	LockedRegion        string
	LockedNode          string
	ConnectionMode      string
	StrictPrivacy       bool
	WintunPath          string
	AutoMergeHours      int
	SocksPort           int
	RuntimeLockedRegion string
	RuntimeLockedNode   string
}
type serviceControlCommand struct {
	Action uint8
	Mask   uint64
	Value  serviceControlState
}

const (
	controlImportPolicy uint64 = 1 << iota
	controlGameBypass
	controlDomesticWeb
	controlWebRTCProtection
	controlDNSProtection
	controlSubscriptionURL
	controlMihomoPath
	controlSelectedNode
	controlSelectionMode
	controlLockedRegion
	controlLockedNode
	controlConnectionMode
	controlStrictPrivacy
	controlWintunPath
	controlAutoMergeHours
	controlSocksPort
	controlRuntimeLockedRegion
	controlRuntimeLockedNode
	controlAll = (1 << 18) - 1
)

func applyServiceControl(s *serviceControlState, c serviceControlCommand) error {
	if (c.Action != 1 && c.Action != 2) || c.Mask&^uint64(controlAll) != 0 || (c.Action == 1 && c.Mask != controlAll) {
		return errors.New("invalid control command")
	}
	v := *s
	if c.Mask&controlImportPolicy != 0 {
		v.ImportPolicy = c.Value.ImportPolicy
	}
	if c.Mask&controlGameBypass != 0 {
		v.GameBypass = nil
		if c.Value.GameBypass != nil {
			v.GameBypass = append([]string{}, c.Value.GameBypass...)
		}
	}
	if c.Mask&controlDomesticWeb != 0 {
		v.DomesticWeb = c.Value.DomesticWeb
	}
	if c.Mask&controlWebRTCProtection != 0 {
		v.WebRTCProtection = c.Value.WebRTCProtection
	}
	if c.Mask&controlDNSProtection != 0 {
		v.DNSProtection = c.Value.DNSProtection
	}
	if c.Mask&controlSubscriptionURL != 0 {
		v.SubscriptionURL = c.Value.SubscriptionURL
	}
	if c.Mask&controlMihomoPath != 0 {
		v.MihomoPath = c.Value.MihomoPath
	}
	if c.Mask&controlSelectedNode != 0 {
		v.SelectedNode = c.Value.SelectedNode
	}
	if c.Mask&controlSelectionMode != 0 {
		v.SelectionMode = c.Value.SelectionMode
	}
	if c.Mask&controlLockedRegion != 0 {
		v.LockedRegion = c.Value.LockedRegion
	}
	if c.Mask&controlLockedNode != 0 {
		v.LockedNode = c.Value.LockedNode
	}
	if c.Mask&controlConnectionMode != 0 {
		v.ConnectionMode = c.Value.ConnectionMode
	}
	if c.Mask&controlStrictPrivacy != 0 {
		v.StrictPrivacy = c.Value.StrictPrivacy
	}
	if c.Mask&controlWintunPath != 0 {
		v.WintunPath = c.Value.WintunPath
	}
	if c.Mask&controlAutoMergeHours != 0 {
		v.AutoMergeHours = c.Value.AutoMergeHours
	}
	if c.Mask&controlSocksPort != 0 {
		v.SocksPort = c.Value.SocksPort
	}
	if c.Mask&controlRuntimeLockedRegion != 0 {
		v.RuntimeLockedRegion = c.Value.RuntimeLockedRegion
	}
	if c.Mask&controlRuntimeLockedNode != 0 {
		v.RuntimeLockedNode = c.Value.RuntimeLockedNode
	}
	if _, e := encodeServiceControlState(v); e != nil {
		return e
	}
	*s = v
	return nil
}

func controlFromSettings(v settings) serviceControlState {
	return serviceControlState{AppGroups: v.AppGroups, ImportPolicy: v.ImportPolicy, GameBypass: v.GameBypass, DomesticWeb: v.DomesticWeb, WebRTCProtection: v.WebRTCProtection, DNSProtection: v.DNSProtection, SubscriptionURL: v.SubscriptionURL, MihomoPath: v.MihomoPath, SelectedNode: v.SelectedNode, SelectionMode: v.SelectionMode, LockedRegion: v.LockedRegion, LockedNode: v.LockedNode, ConnectionMode: v.ConnectionMode, StrictPrivacy: v.StrictPrivacy, WintunPath: v.WintunPath, AutoMergeHours: v.AutoMergeHours, SocksPort: v.SocksPort}
}
func settingsFromControl(v serviceControlState) settings {
	var out settings
	out.ImportPolicy = v.ImportPolicy
	out.AppGroups = v.AppGroups
	out.GameBypass = v.GameBypass
	out.DomesticWeb = v.DomesticWeb
	out.WebRTCProtection = v.WebRTCProtection
	out.DNSProtection = v.DNSProtection
	out.SubscriptionURL = v.SubscriptionURL
	out.MihomoPath = v.MihomoPath
	out.SelectedNode = v.SelectedNode
	out.SelectionMode = v.SelectionMode
	out.LockedRegion = v.LockedRegion
	out.LockedNode = v.LockedNode
	out.ConnectionMode = v.ConnectionMode
	out.StrictPrivacy = v.StrictPrivacy
	out.WintunPath = v.WintunPath
	out.AutoMergeHours = v.AutoMergeHours
	out.SocksPort = v.SocksPort
	return out
}
func (a *app) controlLocked() serviceControlState {
	s := a.serviceStateLocked()
	if s.closed || s.pending != nil || s.catalogFault != nil {
		panic("control authority unavailable")
	}
	p, e := s.backend.ControlQuery()
	if e != nil {
		panic(e)
	}
	v, e := decodeServiceControlState(p)
	if e != nil {
		panic(e)
	}
	v.AppGroups = a.appGroups.clone()
	return v
}
func (a *app) settingsLocked() settings {
	v := settingsFromControl(a.controlLocked())
	chains := a.borrowChainsLocked()
	v.Chain, v.Chains = chains.Chain, chains.Chains
	return v
}
func (a *app) patchControlLocked(mask uint64, v serviceControlState) {
	if mask == 0 {
		return
	}
	p, e := encodeServiceControlCommand(serviceControlCommand{Action: 2, Mask: mask, Value: v})
	if e != nil {
		panic(e)
	}
	if e = a.serviceStateLocked().mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
		panic(e)
	}
}
func (a *app) restoreDecodedControlLocked(v settings) {
	old := a.controlLocked()
	next := controlFromSettings(v)
	next.RuntimeLockedRegion = old.RuntimeLockedRegion
	next.RuntimeLockedNode = old.RuntimeLockedNode
	p, e := encodeServiceControlCommand(serviceControlCommand{Action: 1, Mask: controlAll, Value: next})
	if e != nil {
		panic(e)
	}
	if e = a.serviceStateLocked().mutate(serviceStateCommand{Kind: 5, ControlPayload: p}); e != nil {
		panic(e)
	}
	a.appGroups = v.AppGroups.clone()
}
func (a *app) applySettingsControlDiffLocked(before, after settings) {
	a.applySettingsControlPatchLocked(before, after, 0, "", "")
}
func (a *app) applySettingsControlPatchLocked(before, after settings, mask uint64, runtimeRegion, runtimeNode string) {
	if before.ImportPolicy != after.ImportPolicy {
		mask |= controlImportPolicy
	}
	if !reflect.DeepEqual(before.GameBypass, after.GameBypass) {
		mask |= controlGameBypass
	}
	if before.DomesticWeb != after.DomesticWeb {
		mask |= controlDomesticWeb
	}
	if before.WebRTCProtection != after.WebRTCProtection {
		mask |= controlWebRTCProtection
	}
	if before.DNSProtection != after.DNSProtection {
		mask |= controlDNSProtection
	}
	if before.SubscriptionURL != after.SubscriptionURL {
		mask |= controlSubscriptionURL
	}
	if before.MihomoPath != after.MihomoPath {
		mask |= controlMihomoPath
	}
	if before.SelectedNode != after.SelectedNode {
		mask |= controlSelectedNode
	}
	if before.SelectionMode != after.SelectionMode {
		mask |= controlSelectionMode
	}
	if before.LockedRegion != after.LockedRegion {
		mask |= controlLockedRegion
	}
	if before.LockedNode != after.LockedNode {
		mask |= controlLockedNode
	}
	if before.ConnectionMode != after.ConnectionMode {
		mask |= controlConnectionMode
	}
	if before.StrictPrivacy != after.StrictPrivacy {
		mask |= controlStrictPrivacy
	}
	if before.WintunPath != after.WintunPath {
		mask |= controlWintunPath
	}
	if before.AutoMergeHours != after.AutoMergeHours {
		mask |= controlAutoMergeHours
	}
	if before.SocksPort != after.SocksPort {
		mask |= controlSocksPort
	}
	v := controlFromSettings(after)
	v.RuntimeLockedRegion = runtimeRegion
	v.RuntimeLockedNode = runtimeNode
	a.patchControlLocked(mask, v)
	if !reflect.DeepEqual(before.AppGroups, after.AppGroups) {
		a.appGroups = after.AppGroups.clone()
	}
}
func (a *app) applyRuntimePinsDiffLocked(br, bn, ar, an string) {
	var mask uint64
	if br != ar {
		mask |= controlRuntimeLockedRegion
	}
	if bn != an {
		mask |= controlRuntimeLockedNode
	}
	a.patchControlLocked(mask, serviceControlState{RuntimeLockedRegion: ar, RuntimeLockedNode: an})
}
func (b *goServiceStateBackend) ControlQuery() (string, error) {
	if b.closed {
		return "", errServiceStateClosed
	}
	return encodeServiceControlState(b.control)
}

// Scalar projection deliberately excludes GameBypass; never use it for restore.
func (b *goServiceStateBackend) ControlMeta() (string, error) {
	if b.closed {
		return "", errServiceStateClosed
	}
	v := b.control
	v.GameBypass = nil
	return encodeServiceControlState(v)
}
func (a *app) controlMetaLocked() serviceControlState {
	s := a.serviceStateLocked()
	if s.closed || s.pending != nil || s.catalogFault != nil {
		panic("control authority unavailable")
	}
	p, e := s.backend.ControlMeta()
	if e != nil {
		panic(e)
	}
	v, e := decodeServiceControlState(p)
	if e != nil {
		panic(e)
	}
	return v
}
