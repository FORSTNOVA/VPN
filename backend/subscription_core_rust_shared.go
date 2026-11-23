//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"encoding/binary"
	"errors"
	"runtime"
	"unsafe"
)

func rustSubscriptionAvailable() bool { return rustCoreAvailable() && rustCore.subscription != 0 }

// Never changes app state; malformed/unsupported bridge results use the Go path.
func rustSubscriptionCall(op uint32, input string) ([]byte, int32, bool) {
	if !rustSubscriptionAvailable() || len(input) > 8*1024*1024 {
		return nil, 0, false
	}
	data := []byte(input)
	var ptr *byte
	if len(data) > 0 {
		ptr = &data[0]
	}
	output := make([]byte, len(data)+256)
	for attempt := 0; attempt < 2; attempt++ {
		var written uintptr
		code, _, _ := nativeCall(rustCore.subscription, uintptr(op), uintptr(unsafe.Pointer(ptr)), uintptr(len(data)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(data)
		runtime.KeepAlive(output)
		status := int32(code)
		if status == -2 && written > uintptr(len(output)) && written <= 16*1024*1024 {
			output = make([]byte, int(written))
			continue
		}
		if written > uintptr(len(output)) || (status != 0 && status != 1) {
			return nil, 0, false
		}
		return output[:written], status, true
	}
	return nil, 0, false
}
func parseSubscriptionUserInfo(header string) *subscriptionUserInfo {
	out, status, ok := rustSubscriptionCall(7, header)
	if ok && status == 0 {
		if len(out) == 0 {
			return nil
		}
		if len(out) == 32 {
			word := func(i int) int64 { return int64(binary.LittleEndian.Uint64(out[i:])) }
			return &subscriptionUserInfo{word(0), word(8), word(16), word(24)}
		}
	}
	return parseSubscriptionUserInfoGo(header)
}
func mergeRuleTarget(rule string) (int, int, bool) {
	out, status, ok := rustSubscriptionCall(8, rule)
	if ok && status == 0 {
		if len(out) == 0 {
			return 0, 0, false
		}
		if len(out) == 16 {
			start, end := binary.LittleEndian.Uint64(out), binary.LittleEndian.Uint64(out[8:])
			if start > 0 && start <= end && end <= uint64(len(rule)) {
				return int(start), int(end), true
			}
		}
	}
	return mergeRuleTargetGo(rule)
}

func decodeSubscriptionBase64(input string) ([]byte, error) {
	if out, status, ok := rustSubscriptionCall(1, input); ok {
		if status == 1 {
			return nil, errors.New(string(out))
		}
		return out, nil
	}
	return decodeSubscriptionBase64Go(input)
}
func rustSubscriptionBool(op uint32, input string, fallback func(string) bool) bool {
	if out, status, ok := rustSubscriptionCall(op, input); ok && status == 0 && len(out) == 1 {
		if out[0] == 1 {
			return true
		}
		if out[0] == 0 {
			return false
		}
	}
	return fallback(input)
}
func isProxyURILink(input string) bool { return rustSubscriptionBool(2, input, isProxyURILinkGo) }
func hasProxyURI(input string) bool    { return rustSubscriptionBool(3, input, hasProxyURIGo) }
func isInformationalNode(input string) bool {
	return rustSubscriptionBool(4, input, isInformationalNodeGo)
}
func isInlineSubscription(input string) bool {
	return rustSubscriptionBool(5, input, isInlineSubscriptionGo)
}
func categorizeSubscriptionInput(input string) ([]string, []string) {
	out, status, ok := rustSubscriptionCall(6, input)
	if !ok || status != 0 {
		return categorizeSubscriptionInputGo(input)
	}
	offset := 0
	read := func() ([]string, bool) {
		if len(out)-offset < 8 {
			return nil, false
		}
		count := binary.LittleEndian.Uint64(out[offset:])
		offset += 8
		if count > uint64((len(out)-offset)/8) {
			return nil, false
		}
		var values []string
		for i := uint64(0); i < count; i++ {
			if len(out)-offset < 8 {
				return nil, false
			}
			size := binary.LittleEndian.Uint64(out[offset:])
			offset += 8
			if size > uint64(len(out)-offset) {
				return nil, false
			}
			values = append(values, string(out[offset:offset+int(size)]))
			offset += int(size)
		}
		return values, true
	}
	remote, a := read()
	inline, b := read()
	if !a || !b || offset != len(out) {
		return categorizeSubscriptionInputGo(input)
	}
	return remote, inline
}
