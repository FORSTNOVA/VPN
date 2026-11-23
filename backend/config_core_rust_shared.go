//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

var configRust struct {
	once sync.Once
	proc uintptr
}

func configRustAvailable() bool {
	configRust.once.Do(func() {
		if rustCoreAvailable() {
			version, err := nativeLookup(rustCore.handle, "smartvpn_config_version_v2")
			if err != nil {
				return
			}
			v, _, _ := nativeCall(version)
			if v != 2 {
				return
			}
			configRust.proc, _ = nativeLookup(rustCore.handle, "smartvpn_config_v2")
		}
	})
	return configRust.proc != 0
}
func rustConfigInput(o configOptions, android bool, filters []string) []byte {
	var flags uint32
	for n, v := range []bool{o.TUN, o.DNSProtection, android, o.SourceRuleOrder, o.SubscriptionProvider, o.ManualProvider, o.ChinaDirect, o.DomesticWeb, o.WebRTCProtection, len(o.EntryDNS) > 0, o.ClashRS} {
		if v {
			flags |= 1 << n
		}
	}
	fields := []string{strconv.Itoa(o.MixedPort), strconv.Itoa(o.ControllerPort), findProcessMode, strconv.Quote(o.ControllerSecret), "", entryDNSSection(o.EntryDNS), string(o.ChainProxies), strconv.Quote(o.ProviderPath), strconv.Quote(manualProviderPath), strconv.Quote(filepath.Join("providers", chinaIPListFile)), o.dnsRelayAddr, dnsListenLine, tunFakeIPv4Range, manualProviderName, chainExit, chinaProviderName, strconv.FormatUint(uint64(flags), 10)}
	stack, device, mtu, fd := configTUNFacts()
	fields = append(fields, stack, device, strconv.Itoa(mtu), strconv.Itoa(fd))
	quote := func(values []string) []string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = strconv.Quote(v)
		}
		return out
	}
	var games []string
	for _, v := range o.GameBypass {
		if rule := windowsGameRule(v); rule != "" {
			games = append(games, rule)
		}
	}
	lists := [][]string{quote(filters), strings.Fields(domesticDNSSuffixes), dohIPs[:], quote(games), quote(o.Rules)}
	var wire []byte
	for _, f := range fields {
		wire = appendNodeField(wire, f)
	}
	for _, list := range lists {
		wire = appendNodeField(wire, strconv.Itoa(len(list)))
		for _, v := range list {
			wire = appendNodeField(wire, v)
		}
	}
	presence := "0"
	if o.Rules != nil {
		presence = "1"
	}
	wire = appendNodeField(wire, presence)
	for _, rule := range o.Rules {
		parts := strings.Split(rule, ",")
		wire = appendNodeField(wire, strconv.Itoa(len(parts)))
		for _, part := range parts {
			wire = appendNodeField(wire, strconv.Quote(strings.TrimSpace(part)))
		}
	}
	return wire
}
func rustConfig(op uintptr, o configOptions, android bool, filters []string) (string, bool) {
	if !configRustAvailable() {
		return "", false
	}
	input := rustConfigInput(o, android, filters)
	if len(input) > 8<<20 {
		return "", false
	}
	output := make([]byte, 4096)
	for attempt := 0; attempt < 2; attempt++ {
		var written uintptr
		status, _, _ := nativeCall(configRust.proc, op, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(input)
		runtime.KeepAlive(output)
		if int32(status) == 0 && written <= uintptr(len(output)) {
			return string(output[:written]), true
		}
		if int32(status) != -2 || written > 16<<20 || written <= uintptr(len(output)) {
			return "", false
		}
		output = make([]byte, int(written))
	}
	return "", false
}
func generatedConfig(o configOptions) string {
	if s, ok := rustConfig(0, o, runtime.GOOS == "android", o.ProxyServerDomains); ok {
		return s
	}
	return generatedConfigGo(o)
}
func writeDomesticTCPRules(b *strings.Builder, o configOptions) {
	if s, ok := rustConfig(3, o, runtime.GOOS == "android", nil); ok {
		b.WriteString(s)
		return
	}
	writeDomesticTCPRulesGo(b, o)
}
func dnsConfigSection(filters []string, strict, domesticWeb, android bool, relay string) string {
	o := configOptions{dnsRelayAddr: relay, trafficPolicy: trafficPolicy{DNSProtection: strict, DomesticWeb: domesticWeb}}
	if s, ok := rustConfig(1, o, android, filters); ok {
		return s
	}
	return dnsConfigSectionGo(filters, strict, domesticWeb, android, relay)
}
func dnsUpstreams(strict, domesticWeb bool, relay string) string {
	o := configOptions{dnsRelayAddr: relay, trafficPolicy: trafficPolicy{DNSProtection: strict, DomesticWeb: domesticWeb}}
	if s, ok := rustConfig(2, o, false, nil); ok {
		return s
	}
	return dnsUpstreamsGo(strict, domesticWeb, relay)
}

func tunConfigSection(strict bool) string {
	o := configOptions{trafficPolicy: trafficPolicy{DNSProtection: strict}}
	if s, ok := rustConfig(4, o, runtime.GOOS == "android", nil); ok {
		return s
	}
	return tunConfigSectionGo(strict)
}
