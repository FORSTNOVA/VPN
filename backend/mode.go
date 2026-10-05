package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// The connection modes. System proxy and TUN are the two ways Windows can send
// traffic to the kernel; VPN is the Android one, where the platform's own VPN
// service owns the routes and this service only feeds it.
const (
	modeSystemProxy = "system-proxy"
	modeTUN         = "tun"
	modeVPN         = "vpn"
)

// connectionModeLocked resolves the mode in use. A setting this platform does
// not offer — a profile written on the other one — falls back to its own
// default rather than being refused, so a synced profile cannot leave the app
// unable to connect.
func (a *app) connectionModeLocked() string {
	if mode := a.settings.ConnectionMode; connectionModeSupported(mode) {
		return mode
	}
	return defaultConnectionMode()
}

func tunReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
