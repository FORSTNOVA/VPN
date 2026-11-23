//go:build windows && amd64 && (rustcore || ruststore)

package main

import (
	"golang.org/x/sys/windows"
	"syscall"
)

type nativeHandle = windows.Handle

func loadStoreLibrary(path string) (nativeHandle, error) { return nativeLoad(path) }

func nativeLoad(path string) (nativeHandle, error) {
	if !nativeLibraryPathAllowed(path) {
		return 0, syscall.EINVAL
	}
	return windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
}
func nativeLookup(h nativeHandle, name string) (uintptr, error) {
	if h == 0 || !nativeSymbolAllowed(name) {
		return 0, syscall.EINVAL
	}
	return windows.GetProcAddress(h, name)
}
func nativeClose(h nativeHandle) error { return windows.FreeLibrary(h) }
func nativeCoreCompatible(h nativeHandle) error {
	if releaseBackend != "rust" {
		return nil
	}
	v, err := nativeLookup(h, "smartvpn_service_state_version_v6")
	if err != nil {
		return err
	}
	c, err := nativeLookup(h, "smartvpn_service_state_capabilities_v6")
	if err != nil {
		return err
	}
	version, _, _ := nativeCall(v)
	caps, _, _ := nativeCall(c)
	if version != 6 || caps&63 != 63 {
		return syscall.EINVAL
	}
	return nil
}

//go:uintptrescapes
func nativeCall(proc uintptr, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
	if !nativeInvocationAllowed(proc, len(args)) {
		return ^uintptr(0), 0, syscall.EINVAL
	}
	return syscall.SyscallN(proc, args...)
}
