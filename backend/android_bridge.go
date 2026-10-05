//go:build android

package main

/*
#include <stdlib.h>

// The JNI side lives in android_jni_android.c, so that the Go side deals in
// strings and integers and never in a JNIEnv. These are the calls that go the
// other way: sealing a secret with the Android keystore, and protecting a socket
// so the kernel's own connections do not re-enter the tunnel they carry.
int smartvpn_bridge_init(const char *className);
int smartvpn_protect(int fd);
char *smartvpn_seal(const char *plain);
char *smartvpn_open(const char *sealed);
void smartvpn_drop_tunnel(void);
char *smartvpn_describe_tunnel(void);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"log"
	"syscall"
	"unsafe"

	"github.com/metacubex/mihomo/component/dialer"
)

// SmartVPNStart is the first thing the Java side calls: it prepares the profile
// and starts the local API, and answers with the address the window needs. The
// answer is a bootstrap record as JSON — the same one the Windows service
// prints for its window — so that a refusal travels back as a reason rather
// than as a port that never arrives.
//
//export SmartVPNStart
func SmartVPNStart(dataDir *C.char, bridgeClass *C.char) *C.char {
	if rc := C.smartvpn_bridge_init(bridgeClass); rc != 0 {
		return bridgeReply(bootstrap{Error: "无法连接 Java 桥：订阅凭据的加密与 socket 保护都不可用"})
	}
	// Every socket the kernel dials has to be kept out of the tunnel it carries.
	// Setting this before the kernel starts is the whole of the loop protection;
	// the hook itself is below.
	dialer.DefaultSocketHook = protectSocket

	serviceMu.Lock()
	defer serviceMu.Unlock()
	if service != nil {
		return bridgeReply(bootstrap{Port: service.apiPort, Token: serviceToken})
	}
	started, err := startAndroidService(C.GoString(dataDir))
	if err != nil {
		log.Printf("could not start the service: %v", err)
		return bridgeReply(bootstrap{Error: err.Error()})
	}
	return bridgeReply(started)
}

// SmartVPNStop ends the service and the connection with it.
//
//export SmartVPNStop
func SmartVPNStop() { stopAndroidService() }

// tunnelReport is what the Java side knows about the tunnel and this side does
// not: the interface the system made, the routes it put on it and the resolver
// it points at.
type tunnelReport struct {
	Interface string   `json:"interface"`
	Routes    []string `json:"routes"`
	DNS       []string `json:"dns"`
}

// SmartVPNSetTunnel records the tunnel the VpnService established, or reports
// that it is gone.
//
// The tunnel is the user's to turn on and off, and the system's to take away:
// turning the VPN off from the system's own settings screen stops this app's
// VpnService without the window being involved. A kernel left running against a
// descriptor that no longer exists would look connected while carrying nothing,
// so losing the tunnel ends the connection rather than being recorded as a
// detail.
//
//export SmartVPNSetTunnel
func SmartVPNSetTunnel(authorized C.int, fd C.int, info *C.char) *C.char {
	report := tunnelReport{}
	if err := json.Unmarshal([]byte(C.GoString(info)), &report); err != nil {
		log.Printf("could not read the tunnel description: %v", err)
	}
	if authorized != 0 {
		tunnel.grant(true, int(fd), report.Interface, report.Routes, report.DNS)
	} else {
		tunnel.release()
	}

	serviceMu.Lock()
	a := service
	serviceMu.Unlock()
	if a == nil {
		// A tunnel with nothing behind it is worse than no tunnel: every app's
		// traffic would be captured and dropped. The Java side closes it when it
		// is told this.
		if authorized != 0 {
			return bridgeReply(bootstrap{Error: "本地服务没有运行，隧道已被拒绝"})
		}
		return bridgeReply(nil)
	}
	a.mu.Lock()
	// The permission the tunnel needs on this platform is the user's
	// authorisation, and the window shows it where the Windows copy shows an
	// administrator token.
	a.elevated = authorized != 0
	connected := a.kernelRunningLocked()
	a.mu.Unlock()
	if authorized == 0 && connected {
		a.mu.Lock()
		err := a.dropConnectionLocked()
		a.mu.Unlock()
		if err != nil {
			log.Printf("could not end the connection after the tunnel went away: %v", err)
		}
	}
	return bridgeReply(nil)
}

// bridgeReply renders an answer for the Java side, which reads JSON and treats
// nothing at all as a failure it cannot explain.
func bridgeReply(value any) *C.char {
	if value == nil {
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		log.Printf("could not render the answer to the Java side: %v", err)
		return nil
	}
	return C.CString(string(body))
}

// protectSocket keeps the kernel's own connections out of the kernel's tunnel.
//
// On Android a VPN captures the traffic of the app that created it like any
// other app's, so a socket the kernel opens to reach a proxy server would be
// routed straight back into the tunnel that connection is supposed to carry,
// and nothing would ever leave the device. VpnService.protect is the platform's
// answer: it binds one socket to the underlying network, and every socket the
// kernel dials goes through here to get it.
func protectSocket(network, address string, conn syscall.RawConn) error {
	refused := false
	if err := conn.Control(func(fd uintptr) {
		if C.smartvpn_protect(C.int(fd)) != 0 {
			refused = true
		}
	}); err != nil {
		return err
	}
	if refused {
		return errors.New("the system refused to protect a socket; it would be routed into the tunnel it carries")
	}
	return nil
}

// jniSeal and jniOpen are the keystore. Its key belongs to the system and is
// never readable from this process, which is why the work happens on the Java
// side and only the plaintext ever crosses.
func jniSeal(plain string) (string, error) {
	return jniStringCall(plain, func(value *C.char) *C.char { return C.smartvpn_seal(value) })
}

func jniOpen(sealed string) (string, error) {
	return jniStringCall(sealed, func(value *C.char) *C.char { return C.smartvpn_open(value) })
}

func jniStringCall(input string, call func(*C.char) *C.char) (string, error) {
	value := C.CString(input)
	defer C.free(unsafe.Pointer(value))
	result := call(value)
	if result == nil {
		return "", errors.New("the keystore could not be reached")
	}
	defer C.free(unsafe.Pointer(result))
	return C.GoString(result), nil
}

// jniDescribeTunnel asks the Java side for the tunnel's description as it stands
// now: the interface, the routes on it and the resolver it points at.
func jniDescribeTunnel() (tunnelReport, bool) {
	raw := C.smartvpn_describe_tunnel()
	if raw == nil {
		return tunnelReport{}, false
	}
	defer C.free(unsafe.Pointer(raw))
	report := tunnelReport{}
	if json.Unmarshal([]byte(C.GoString(raw)), &report) != nil {
		return tunnelReport{}, false
	}
	return report, true
}

// jniDropTunnel asks the Java side to take the tunnel away. It is called when
// the kernel has gone and the interface it was reading has nobody left to read
// it: stopping the service is what removes a VpnService's tunnel.
func jniDropTunnel() { C.smartvpn_drop_tunnel() }
