//go:build android && cgo

package main

/*
#include <stddef.h>
#include <string.h>
static size_t smartvpn_config_path_len(const char *p) { return p ? strnlen(p,4097) : 0; }
*/
import "C"
import (
	"errors"
	"path/filepath"
)

// SmartVPNRustCoreConfigure sets an explicitly selected packaged Rust core before
// SmartVPNStart. 0 success; -1 invalid input; -2 frozen/active; -3 environment
// error; -4 this build has no native Rust bridge. It never starts or loads a core.
// A C string cannot represent embedded NUL; length-aware callers use ConfigureN.
//
//export SmartVPNRustCoreConfigure
func SmartVPNRustCoreConfigure(path *C.char, enabled C.int) C.int {
	return SmartVPNRustCoreConfigureN(path, C.size_t(C.smartvpn_config_path_len(path)), enabled)
}

//export SmartVPNRustCoreConfigureN
func SmartVPNRustCoreConfigureN(path *C.char, length C.size_t, enabled C.int) C.int {
	if enabled != 0 && enabled != 1 || length > 4096 || path == nil && length != 0 {
		return -1
	}

	value := ""
	if length != 0 {
		value = C.GoStringN(path, C.int(length))
	}
	if enabled == 1 && filepath.Base(value) != "libsmartvpn_core.so" {
		return -1
	}
	if err := nativeConfigurationAllowed(nativeCoreConfiguration{}, value, enabled == 1, false); err != nil {
		return -1
	}
	if !nativeCoreBuildEnabled {
		if enabled == 0 {
			return 0
		}
		return -4
	}
	serviceMu.Lock()
	defer serviceMu.Unlock()
	err := nativeConfigureCore(value, enabled == 1, service != nil || serviceAPI != nil)
	if errors.Is(err, errNativeConfigurationInvalid) {
		return -1
	}
	if errors.Is(err, errNativeConfigurationFrozen) {
		return -2
	}
	if err != nil {
		return -3
	}
	return 0
}
