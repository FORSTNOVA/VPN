//go:build windows

package main

import (
	"github.com/metacubex/mihomo/component/resolver"
	"log"
	"os"
	"path/filepath"
)

func (a *app) platformAPIRoutes(routes []apiRoute) []apiRoute {
	return append(routes, apiRoute{"POST", 50, "/api/tun/wintun", a.installWintun})
}

// The standalone Windows probe uses Mihomo without applying a kernel config.
// Match the live kernel's default dual-stack outbound resolution before serving requests.
func init() { resolver.DisableIPv6 = false }

// Set by the distribution build. Developer builds retain explicit selection.
var releaseBackend string

func configureWindowsRelease() error {
	if releaseBackend == "" {
		return nil
	}
	path := nativeCoreSelectedPath(os.Getenv)
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		path = filepath.Join(filepath.Dir(exe), "smartvpn_core.dll")
	}
	useRust := releaseBackend == "rust" && os.Getenv("SMARTVPN_BACKEND") != "go" && nativeLibraryPathAllowed(path)
	if !useRust {
		path = ""
	}
	if err := nativeConfigureCore(path, useRust, false); err != nil {
		return err
	}
	if useRust && packagedCoreLoaded() {
		log.Print("distribution backend: Rust")
		return nil
	}
	log.Print("distribution backend: Go")
	if err := os.Setenv("SMARTVPN_MANAGED_HOST", "off"); err != nil {
		return err
	}
	return os.Setenv("SMARTVPN_STORE_BACKEND", "go")
}
func main() {
	if err := configureWindowsRelease(); err != nil {
		log.Fatal(err)
	}
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "windows", Wait: serviceLockWait, ShutdownWait: serviceShutdownWait})
	if r := runBootstrapWithEffects(e.q, e); r.Err != nil {
		log.Fatalf("%v", r.Err)
	}
}
