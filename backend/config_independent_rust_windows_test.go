//go:build windows && amd64 && rustcore

package main

import (
	"encoding/binary"
	"fmt"
	"golang.org/x/sys/windows"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

type configIndependentRule struct {
	raw   string
	class int
}

func configIndependentRules() []configIndependentRule {
	return []configIndependentRule{
		{"DOMAIN,a.invalid,DIRECT", 1}, {"DOMAIN,b.invalid,REJECT", 2}, {"DOMAIN,c.invalid,SmartVPN", 0},
		{"MATCH,DIRECT", 0}, {"", 0}, {",,DIRECT", 1}, {"DOMAIN,x, REJECT \t,other", 2},
		{"AND,((NETWORK,tcp),(DST-PORT,443)),REJECT", 0}, {"A,B,REJECT,anything", 2},
		{"A,B,\u0085DIRECT\u2003", 1}, {"A,B,D陌RECT", 0}, {"A,B,direct", 0}, {"A,B,REJECT\u200b", 0},
		{"A,B,REJECT\xff", 0}, {"\xff,A,REJECT", 2}, {"A,\"B,C\",DIRECT", 0},
		{"A,B,REJECT\n", 2}, {"A,B,DIRECT\x00", 0}, {"A,B,REJECT", 2},
	}
}
func configIndependentRuleSection(rs []configIndependentRule, private, source, webrtc, chain bool) string {
	var b strings.Builder
	b.WriteString("rules:\n")
	emit := func(r string) { fmt.Fprintf(&b, "  - %s\n", strconv.Quote(r)) }
	if private {
		for _, r := range rs {
			if r.class == 2 {
				emit(r.raw)
			}
		}
		if !webrtc {
			emit("AND,((NETWORK,udp),(DST-PORT,443)),REJECT")
		}
		if source {
			for _, r := range rs {
				if r.class == 0 {
					emit(r.raw)
				}
			}
		}
		b.WriteString("  - MATCH,SmartVPN\n")
		if webrtc || chain {
			b.WriteString("  - MATCH,REJECT\n")
		}
		return b.String()
	}
	if source {
		for _, r := range rs {
			emit(r.raw)
		}
		emit("AND,((NETWORK,udp),(DST-PORT,443)),REJECT")
	} else {
		for _, r := range rs {
			if r.class != 0 {
				emit(r.raw)
			}
		}
		emit("AND,((NETWORK,udp),(DST-PORT,443)),REJECT")
		for _, r := range rs {
			if r.class == 0 {
				emit(r.raw)
			}
		}
	}
	b.WriteString("  - MATCH,SmartVPN\n")
	if chain {
		b.WriteString("  - MATCH,REJECT\n")
	}
	return b.String()
}
func TestConfigIndependentRulesActual(t *testing.T) {
	if !configRustAvailable() {
		t.Fatal("config v2 unavailable; strict actual test")
	}
	rng := rand.New(rand.NewSource(4103))
	pool := configIndependentRules()
	for n := 0; n < 512; n++ {
		bits := n % 32
		var rs []configIndependentRule
		if n > 1 {
			for j := 0; j < rng.Intn(40)+1; j++ {
				rs = append(rs, pool[rng.Intn(len(pool))])
			}
		}
		o := configOptions{TUN: bits&1 != 0, SourceRuleOrder: bits&2 != 0, trafficPolicy: trafficPolicy{DNSProtection: bits&4 != 0, WebRTCProtection: bits&8 != 0}}
		if n == 1 {
			o.Rules = []string{}
		}
		for _, r := range rs {
			o.Rules = append(o.Rules, r.raw)
		}
		if bits&16 != 0 {
			o.ChainProxies = []byte("proxies: []\n")
		}
		got, ok := rustConfig(0, o, false, nil)
		if !ok {
			t.Fatalf("normal raw rule fallback %d", n)
		}
		want := configIndependentRuleSection(rs, o.TUN && (o.DNSProtection || o.WebRTCProtection), o.SourceRuleOrder, o.WebRTCProtection, len(o.ChainProxies) > 0)
		at := strings.LastIndex(got, "rules:\n")
		if at < 0 || got[at:] != want {
			t.Fatalf("rule policy mismatch n=%d got=%q want=%q", n, got[at:], want)
		}
		if got != generatedConfigGo(o) {
			t.Fatalf("whole config byte mismatch %d", n)
		}
	}
}
func configIndependentFields(t *testing.T, b []byte) []string {
	t.Helper()
	var fields []string
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatal("wire header")
		}
		n := binary.LittleEndian.Uint64(b)
		b = b[8:]
		if n > uint64(len(b)) {
			t.Fatal("wire size")
		}
		fields = append(fields, string(b[:n]))
		b = b[n:]
	}
	return fields
}
func configIndependentCall(t *testing.T, proc uintptr, op uintptr, b []byte) (string, int32) {
	t.Helper()
	out := make([]byte, 32)
	for attempts := 0; attempts < 2; attempts++ {
		var n uintptr
		status, _, _ := syscall.SyscallN(proc, op, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&n)))
		runtime.KeepAlive(b)
		runtime.KeepAlive(out)
		if int32(status) == -2 && n > uintptr(len(out)) && n <= 16<<20 {
			out = make([]byte, n)
			continue
		}
		if int32(status) != 0 {
			return "", int32(status)
		}
		if n > uintptr(len(out)) {
			t.Fatal("bad output length")
		}
		return string(out[:n]), 0
	}
	t.Fatal("capacity retry failed")
	return "", -999
}
func TestConfigIndependentTUNActual(t *testing.T) {
	if !configRustAvailable() {
		t.Fatal("config v2 unavailable")
	}
	for _, android := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			for _, fd := range []int64{-1, 0, 37, 9223372036854775807} {
				o := configOptions{trafficPolicy: trafficPolicy{DNSProtection: strict}}
				fs := configIndependentFields(t, rustConfigInput(o, android, nil))
				fs[17] = "synthetic-stack"
				fs[18] = "synthetic-device"
				fs[19] = "-1300"
				fs[20] = strconv.FormatInt(fd, 10)
				var wire []byte
				for _, f := range fs {
					wire = appendNodeField(wire, f)
				}
				got, status := configIndependentCall(t, configRust.proc, 4, wire)
				if status != 0 {
					t.Fatal("TUN facts rejected", status)
				}
				var want string
				if android {
					want = fmt.Sprintf("tun:\n  enable: true\n  stack: synthetic-stack\n  file-descriptor: %d\n  auto-route: false\n  auto-detect-interface: false\n  dns-hijack:\n    - any:53\n    - tcp://any:53\n  mtu: -1300\n", fd)
				} else {
					sr := ""
					if strict {
						sr = "  strict-route: true\n"
					}
					want = "tun:\n  enable: true\n  stack: synthetic-stack\n  device: synthetic-device\n  auto-route: true\n  auto-detect-interface: true\n" + sr + "  dns-hijack:\n    - any:53\n    - tcp://any:53\n  mtu: -1300\n"
				}
				if got != want {
					t.Fatalf("TUN byte mismatch android=%v strict=%v fd=%d got=%q want=%q", android, strict, fd, got, want)
				}
			}
		}
	}
}
func TestConfigIndependentOldV1Fallback(t *testing.T) {
	if os.Getenv("SMARTVPN_TEST_CONFIG_OLD_V1") != "1" {
		t.Skip("explicit old v1 DLL only")
	}
	if !rustCoreAvailable() {
		t.Fatal("old DLL missing")
	}
	if _, err := windows.GetProcAddress(rustCore.handle, "smartvpn_config_v1"); err != nil {
		t.Fatal("not known old config DLL")
	}
	if configRustAvailable() {
		t.Fatal("old config accepted as v2")
	}
	o := configOptions{TUN: true, Rules: []string{"A,B,REJECT", "MATCH,DIRECT"}, trafficPolicy: trafficPolicy{DNSProtection: true}}
	if _, ok := rustConfig(0, o, false, nil); ok {
		t.Fatal("old ABI invoked")
	}
	if generatedConfig(o) != generatedConfigGo(o) {
		t.Fatal("whole-Go fallback byte mismatch")
	}
}

func TestConfigIndependentV1CompatibilityActual(t *testing.T) {
	if !configRustAvailable() {
		t.Fatal("config v2 unavailable")
	}
	v1, err := windows.GetProcAddress(rustCore.handle, "smartvpn_config_v1")
	if err != nil {
		t.Fatal(err)
	}
	for bits := 0; bits < 32; bits++ {
		o := configOptions{TUN: bits&1 != 0, SourceRuleOrder: bits&2 != 0, ChinaDirect: bits&4 != 0, trafficPolicy: trafficPolicy{DNSProtection: bits&8 != 0, WebRTCProtection: bits&16 != 0}}
		rules := configIndependentRules()
		for _, r := range rules {
			o.Rules = append(o.Rules, r.raw)
		}
		all := configIndependentFields(t, rustConfigInput(o, false, nil))
		fields := append([]string{}, all[:17]...)
		fields[4] = tunConfigSectionGo(o.DNSProtection)
		at := 21
		for list := 0; list < 5; list++ {
			n, err := strconv.Atoi(all[at])
			if err != nil {
				t.Fatal(err)
			}
			fields = append(fields, all[at:at+1+n]...)
			at += n + 1
		}
		for _, which := range []int{1, 0, 2} {
			var rs []string
			for _, r := range rules {
				keep := r.class == which
				if which == 1 {
					keep = r.class != 0
				}
				if keep {
					rs = append(rs, strconv.Quote(r.raw))
				}
			}
			fields = append(fields, strconv.Itoa(len(rs)))
			fields = append(fields, rs...)
		}
		var wire []byte
		for _, f := range fields {
			wire = appendNodeField(wire, f)
		}
		got, status := configIndependentCall(t, v1, 0, wire)
		if status != 0 || got != generatedConfigGo(o) {
			t.Fatalf("old v1 export changed bits=%d status=%d", bits, status)
		}
	}
	for _, strict := range []bool{false, true} {
		if tunConfigSection(strict) != tunConfigSectionGo(strict) {
			t.Fatal("platform selector template mismatch")
		}
	}
}
func TestConfigIndependentMalformedActual(t *testing.T) {
	if !configRustAvailable() {
		t.Fatal("config v2 unavailable")
	}
	o := configOptions{Rules: []string{"A,B,REJECT"}}
	base := rustConfigInput(o, false, nil)
	fs := configIndependentFields(t, base)
	encode := func(f []string) []byte {
		var b []byte
		for _, s := range f {
			b = appendNodeField(b, s)
		}
		return b
	}
	var malformed [][]byte
	malformed = append(malformed, base[:len(base)-1], append(append([]byte{}, base...), 0))
	bad := append([]string{}, fs...)
	bad[4] = "old opaque TUN"
	malformed = append(malformed, encode(bad))
	bad = append([]string{}, fs...)
	bad[16] = "1024"
	malformed = append(malformed, encode(bad))
	bad = append([]string{}, fs...)
	bad[21] = "65537"
	malformed = append(malformed, encode(bad))
	at := 21
	for j := 0; j < 5; j++ {
		n, _ := strconv.Atoi(fs[at])
		at += n + 1
	}
	bad = append([]string{}, fs...)
	bad[at] = "0"
	malformed = append(malformed, encode(bad))
	bad = append([]string{}, fs...)
	bad[at] = "2"
	malformed = append(malformed, encode(bad))
	for n, b := range malformed {
		_, status := configIndependentCall(t, configRust.proc, 0, b)
		if status != -1 {
			t.Fatalf("malformed %d accepted status=%d", n, status)
		}
	}
	large := configOptions{Rules: []string{"A," + strings.Repeat("synthetic", 5000) + ",REJECT"}}
	got, ok := rustConfig(0, large, false, nil)
	if !ok || got != generatedConfigGo(large) {
		t.Fatal("pure query capacity retry mismatch")
	}
	invalid := configOptions{ChainProxies: []byte{255, 0, 254}}
	if _, ok := rustConfig(0, invalid, false, nil); ok {
		t.Fatal("raw invalid chain must reject native UTF8 codec")
	}
	if generatedConfig(invalid) != generatedConfigGo(invalid) {
		t.Fatal("invalid chain Go fallback lost original bytes")
	}
}
