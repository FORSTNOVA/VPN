//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import "strings"

func generatedConfig(o configOptions) string                    { return generatedConfigGo(o) }
func writeDomesticTCPRules(b *strings.Builder, o configOptions) { writeDomesticTCPRulesGo(b, o) }
func dnsConfigSection(filters []string, strict, domesticWeb, android bool, relay string) string {
	return dnsConfigSectionGo(filters, strict, domesticWeb, android, relay)
}
func dnsUpstreams(strict, domesticWeb bool, relay string) string {
	return dnsUpstreamsGo(strict, domesticWeb, relay)
}

func tunConfigSection(strict bool) string { return tunConfigSectionGo(strict) }
