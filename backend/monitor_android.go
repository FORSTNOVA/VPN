//go:build android && cgo

package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef void *(*start_fn)(uint16_t, const char *, const char *, uint16_t *);
typedef void (*stop_fn)(void *);
static start_fn start_host, start_reads, start_api;
static stop_fn stop_host;
static int load_host(const char *path) {
    void *lib = dlopen(path, RTLD_NOW | RTLD_LOCAL);
    if (!lib) return 0;
    start_host = (start_fn)dlsym(lib, "smartvpn_host_start_v1");
    start_api = (start_fn)dlsym(lib,"smartvpn_host_api_start_v1");
    start_reads = (start_fn)dlsym(lib, "smartvpn_host_reads_start_v1");
    stop_host = (stop_fn)dlsym(lib, "smartvpn_host_stop_v1");
    if (start_host && stop_host) return 1;
    dlclose(lib);
    return 0;
}
static void *open_monitor(uint16_t port, const char *key, const char *token, uint16_t *bound) {
    return start_host(port, key, token, bound);
}
static void *open_reads(uint16_t port, const char *key, const char *token, uint16_t *bound) {return start_reads ? start_reads(port,key,token,bound) : NULL;}
static void *open_api(uint16_t port,const char *key,const char *token,uint16_t *bound){return start_api ? start_api(port,key,token,bound) : NULL;}
static void close_monitor(void *session) { stop_host(session); }
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"unsafe"
)

var loadAndroidHost = sync.OnceValue(func() bool {
	core := nativeCoreSelectedPath(os.Getenv)
	if !packagedCoreLoaded() || !nativeLibraryPathAllowed(core) || filepath.Base(core) != "libsmartvpn_core.so" {
		return false
	}
	path := C.CString(filepath.Join(filepath.Dir(core), "libsmartvpn_host.so"))
	defer C.free(unsafe.Pointer(path))
	if C.load_host(path) == 0 {
		log.Print("Android Rust monitor unavailable, using local queries")
		return false
	}
	return true
})

// Accessed under the app lock, like the owning kernel runner.
type androidMonitor struct {
	target  monitorTarget
	cancel  context.CancelFunc
	session unsafe.Pointer
}

func (k *embeddedKernel) startMonitor() *androidMonitor {
	if !loadAndroidHost() || k.app.ctrlPort <= 0 || k.app.ctrlPort > 65535 {
		return nil
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil
	}
	token := hex.EncodeToString(key[:])
	auth, secret := C.CString(token), C.CString(k.app.ctrlSecret)
	defer C.free(unsafe.Pointer(auth))
	defer C.free(unsafe.Pointer(secret))
	var port C.uint16_t
	session := C.open_monitor(C.uint16_t(k.app.ctrlPort), secret, auth, &port)
	if session == nil {
		log.Print("Android Rust monitor start failed, using local queries")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &androidMonitor{monitorTarget{"127.0.0.1:" + strconv.Itoa(int(port)), token, ctx}, cancel, session}
}
func (k *embeddedKernel) stopMonitor() {
	if m := k.monitor; m != nil {
		k.monitor = nil
		m.cancel()
		C.close_monitor(m.session)
	}
}
func (k *embeddedKernel) monitorEndpoint() (monitorTarget, bool) {
	if k.monitor == nil {
		return monitorTarget{}, false
	}
	return k.monitor.target, true
}

func startAPIHost(port int, key, token string, writes bool) (string, func(), error) {
	if !loadAndroidHost() {
		return "", nil, errors.New("host unavailable")
	}
	k, t := C.CString(key), C.CString(token)
	defer C.free(unsafe.Pointer(k))
	defer C.free(unsafe.Pointer(t))
	var bound C.uint16_t
	var h unsafe.Pointer
	if writes {
		h = C.open_api(C.uint16_t(port), k, t, &bound)
	} else {
		h = C.open_reads(C.uint16_t(port), k, t, &bound)
	}
	if h == nil {
		return "", nil, errors.New("read host unavailable")
	}
	return "127.0.0.1:" + strconv.Itoa(int(bound)), func() { C.close_monitor(h) }, nil
}
