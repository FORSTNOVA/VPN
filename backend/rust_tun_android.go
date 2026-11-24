//go:build android

package main

/*
#cgo LDFLAGS: -ldl -llog
#include <stdint.h>
#include <stdlib.h>
int smartvpn_rust_tun_validate(const char *);
int64_t smartvpn_rust_tun_start(int, const char *);
int smartvpn_rust_tun_running(int64_t);
int smartvpn_rust_tun_stop(int64_t);
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func rustTunEnabled(home string) (bool, error) {
	if os.Getenv("SMARTVPN_RUST_TUN") == "1" {
		return true, nil
	}
	info, err := os.Stat(filepath.Join(home, "rust_tun.flag"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("rust_tun.flag must be a regular file")
	}
	return true, nil
}

// The app mutex serializes runner methods. The engine duplicates fd during Start.
type rustTunKernel struct {
	app        *app
	handle     C.int64_t
	fd         int
	generation uint64
}

func (*rustTunKernel) standaloneTUN() {}

func (k *rustTunKernel) Running() bool {
	return k.handle > 0 && tunnel.ownedBy(k.generation) && C.smartvpn_rust_tun_running(k.handle) == 1
}

func (k *rustTunKernel) Start() error {
	body, err := os.ReadFile(k.app.configPath())
	if err != nil {
		return err
	}
	data, err := materializeRustTun(k.app.home, body, k.app.selectedChoice())
	if err != nil {
		return err
	}
	yaml := C.CString(string(data))
	defer C.free(unsafe.Pointer(yaml))
	// Grant and release callbacks must not replace this lease between dup and handover.
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	if tunnel.lease.fd <= 0 || tunnel.lease.flags != leaseAuthorized {
		return errors.New("Rust TUN requires an authorized, unclaimed descriptor")
	}
	fd := int(tunnel.lease.fd)
	handle := C.smartvpn_rust_tun_start(C.int(fd), yaml)
	if handle <= 0 {
		return fmt.Errorf("Rust TUN start failed (%d)", handle)
	}
	k.handle, k.fd, k.generation = handle, fd, tunnel.handOverLocked()
	return nil
}

func (k *rustTunKernel) Stop() error {
	if k.handle == 0 {
		return nil
	}
	status := C.smartvpn_rust_tun_stop(k.handle)
	k.handle = 0
	closeErr := syscall.Close(k.fd)
	k.fd = 0
	if status != 0 {
		return errors.Join(fmt.Errorf("Rust TUN stop failed (%d)", status), closeErr)
	}
	return closeErr
}
