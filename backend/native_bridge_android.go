//go:build android && (arm64 || amd64) && cgo && rustcore

package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
_Static_assert(sizeof(uintptr_t)==8,"SmartVPN Rust bridge requires 64-bit pointers");
static uintptr_t smartvpn_native_call(uintptr_t p, int n, const uintptr_t *a) {
 switch(n) {
 case 0:return ((uintptr_t(*)(void))p)();
 case 1:return ((uintptr_t(*)(uintptr_t))p)(a[0]);
 case 2:return ((uintptr_t(*)(uintptr_t,uintptr_t))p)(a[0],a[1]);
 case 3:return ((uintptr_t(*)(uintptr_t,uintptr_t,uintptr_t))p)(a[0],a[1],a[2]);
 case 4:return ((uintptr_t(*)(uintptr_t,uintptr_t,uintptr_t,uintptr_t))p)(a[0],a[1],a[2],a[3]);
 case 5:return ((uintptr_t(*)(uintptr_t,uintptr_t,uintptr_t,uintptr_t,uintptr_t))p)(a[0],a[1],a[2],a[3],a[4]);
 case 6:return ((uintptr_t(*)(uintptr_t,uintptr_t,uintptr_t,uintptr_t,uintptr_t,uintptr_t))p)(a[0],a[1],a[2],a[3],a[4],a[5]);
 case 7:return ((uintptr_t(*)(uintptr_t,uintptr_t,uintptr_t,uintptr_t,uintptr_t,uintptr_t,uintptr_t))p)(a[0],a[1],a[2],a[3],a[4],a[5],a[6]);
 default:return UINTPTR_MAX;
 }
}
*/
import "C"
import (
	"errors"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

type nativeHandle uintptr

func nativeLoad(path string) (nativeHandle, error) {
	return loadNamedAndroidLibrary(path, "libsmartvpn_core.so")
}
func loadStoreLibrary(path string) (nativeHandle, error) {
	return loadNamedAndroidLibrary(path, "libsmartvpn_store.so")
}
func loadNamedAndroidLibrary(path, name string) (nativeHandle, error) {
	if !nativeLibraryPathAllowed(path) || filepath.Base(path) != name {
		return 0, syscall.EINVAL
	}
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	h := C.dlopen(p, C.RTLD_NOW|C.RTLD_LOCAL)
	if h == nil {
		return 0, errors.New("Rust core library could not be loaded")
	}
	return nativeHandle(uintptr(h)), nil
}
func nativeLookup(h nativeHandle, name string) (uintptr, error) {
	if h == 0 || !nativeSymbolAllowed(name) {
		return 0, syscall.EINVAL
	}
	p := C.CString(name)
	defer C.free(unsafe.Pointer(p))
	v := C.dlsym(unsafe.Pointer(uintptr(h)), p)
	if v == nil {
		return 0, errors.New("Rust core symbol unavailable")
	}
	return uintptr(v), nil
}
func nativeClose(h nativeHandle) error {
	if h == 0 {
		return syscall.EINVAL
	}
	if C.dlclose(unsafe.Pointer(uintptr(h))) != 0 {
		return errors.New("Rust core close failed")
	}
	return nil
}
func nativeCoreCompatible(h nativeHandle) error {
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
	if !serviceStateFullCapabilities(uint64(version), uint64(caps)) {
		return errors.New("Rust core version/capabilities mismatch")
	}
	return nil
}

// Calls are synchronous. Rust copies input buffers and never retains Go pointers.
//
//go:uintptrescapes
func nativeCall(proc uintptr, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
	if !nativeInvocationAllowed(proc, len(args)) {
		return ^uintptr(0), 0, syscall.EINVAL
	}
	var a [7]C.uintptr_t
	for i, v := range args {
		a[i] = C.uintptr_t(v)
	}
	out := C.smartvpn_native_call(C.uintptr_t(proc), C.int(len(args)), &a[0])
	runtime.KeepAlive(args)
	return uintptr(out), 0, 0
}
