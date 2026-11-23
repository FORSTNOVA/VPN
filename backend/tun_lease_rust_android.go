//go:build android && rustcore && cgo && (amd64 || arm64)

package main

import (
	"runtime"
	"sync"
	"unsafe"
)

var lookupTunLease = sync.OnceValue(func() uintptr {
	if !rustCoreAvailable() {
		return 0
	}
	proc, _ := nativeLookup(rustCore.handle, "smartvpn_android_tun_lease_v1")
	return proc
})

func (l *androidTunLease) applyRust(op int, fd int32, authorized bool) (int, bool) {
	proc := lookupTunLease()
	if proc == 0 {
		return 0, false
	}
	var flag uintptr
	if authorized {
		flag = 1
	}
	next := *l
	orphan, _, err := nativeCall(proc, uintptr(unsafe.Pointer(&next)), uintptr(op), uintptr(fd), flag)
	runtime.KeepAlive(&next)
	if err != 0 || orphan == ^uintptr(0) {
		return 0, false
	}
	*l = next
	return int(orphan), true
}
