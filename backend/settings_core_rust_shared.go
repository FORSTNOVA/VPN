//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

var settingsRust struct {
	once        sync.Once
	call, socks uintptr
}

func settingsRustAvailable() bool {
	settingsRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		settingsRust.call, _ = nativeLookup(rustCore.handle, "smartvpn_settings_v1")
		settingsRust.socks, _ = nativeLookup(rustCore.handle, "smartvpn_valid_socks_v1")
	})
	return settingsRust.call != 0 && settingsRust.socks != 0
}
func settingFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
func rustSettingsCall(op uint32, values []string) ([]byte, int32, bool) {
	if !settingsRustAvailable() {
		return nil, 0, false
	}
	var input []byte
	for _, v := range values {
		if !utf8.ValidString(v) || len(v) > 8<<20 || len(input) > 8<<20-8-len(v) {
			return nil, 0, false
		}
		input = binary.LittleEndian.AppendUint64(input, uint64(len(v)))
		input = append(input, v...)
	}
	var ptr *byte
	if len(input) > 0 {
		ptr = &input[0]
	}
	out := make([]byte, len(input)+256)
	for i := 0; i < 2; i++ {
		var written uintptr
		code, _, _ := nativeCall(settingsRust.call, uintptr(op), uintptr(unsafe.Pointer(ptr)), uintptr(len(input)), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(input)
		runtime.KeepAlive(out)
		status := int32(code)
		if status == -2 && written > uintptr(len(out)) && written <= 16<<20 {
			out = make([]byte, int(written))
			continue
		}
		if written > uintptr(len(out)) || (status != 0 && status != 1) {
			return nil, 0, false
		}
		return out[:written], status, true
	}
	return nil, 0, false
}
func windowsGameRule(entry string) string {
	if out, status, ok := rustSettingsCall(1, []string{entry, filepath.Clean(entry), settingFlag(gameExeName.MatchString(entry)), settingFlag(filepath.IsAbs(entry) && strings.EqualFold(filepath.Ext(entry), ".exe"))}); ok && status == 0 {
		return string(out)
	}
	return windowsGameRuleGo(entry)
}
func normalizeGameBypass(entries []string, android bool) ([]string, error) {
	if !settingsRustAvailable() {
		return normalizeGameBypassGo(entries, android)
	}
	values := make([]string, 0, len(entries)*3)
	for _, entry := range entries {
		if !utf8.ValidString(entry) {
			return normalizeGameBypassGo(entries, android)
		}
		name := strings.TrimSpace(entry)
		key := name
		valid := true
		if !android {
			valid = windowsGameRule(name) != ""
			if filepath.IsAbs(name) {
				name = filepath.Clean(name)
			}
			key = strings.ToLower(name)
		}
		values = append(values, name, key, settingFlag(valid))
	}
	op := uint32(3)
	if android {
		op = 2
	}
	out, status, ok := rustSettingsCall(op, values)
	if !ok {
		return normalizeGameBypassGo(entries, android)
	}
	if status == 1 {
		return nil, errors.New(string(out))
	}
	result := make([]string, 0)
	for len(out) > 0 {
		if len(out) < 8 {
			return normalizeGameBypassGo(entries, android)
		}
		n := binary.LittleEndian.Uint64(out)
		out = out[8:]
		if n > uint64(len(out)) || !utf8.Valid(out[:n]) {
			return normalizeGameBypassGo(entries, android)
		}
		result = append(result, string(out[:n]))
		out = out[n:]
	}
	return result, nil
}
func validSocksSetting(port int, windows bool) bool {
	if settingsRustAvailable() {
		var flag uintptr
		if windows {
			flag = 1
		}
		value, _, _ := nativeCall(settingsRust.socks, uintptr(port), flag)
		if value <= 1 {
			return value == 1
		}
	}
	return validSocksSettingGo(port, windows)
}
