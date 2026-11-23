package main

import (
	"errors"
	"os"
	"sync"
)

type nativeCoreConfiguration struct{ Configured, LoadStarted bool }

var nativeConfiguration struct {
	sync.Mutex
	state nativeCoreConfiguration
}
var nativeCoreSwitches = []string{
	"SMARTVPN_RUST_ADAPTIVE_FLOW",
	"SMARTVPN_RUST_ANDROID_RESOLVER",
	"SMARTVPN_RUST_APP_FLOW",
	"SMARTVPN_RUST_BOOTSTRAP_FLOW",
	"SMARTVPN_RUST_CHAIN_FLOW",
	"SMARTVPN_RUST_CHAIN_PROBE_FLOW",
	"SMARTVPN_RUST_CHINA_FLOW",
	"SMARTVPN_RUST_CONNECTION_FLOW",
	"SMARTVPN_RUST_CONVERTER_FLOW",
	"SMARTVPN_RUST_DIAGNOSTIC_FLOW",
	"SMARTVPN_RUST_DRIVER_INSTALL",
	"SMARTVPN_RUST_EMBEDDED_KERNEL",
	"SMARTVPN_RUST_EXIT_FLOW",
	"SMARTVPN_RUST_HEALTH_FLOW",
	"SMARTVPN_RUST_HTTP_DISPATCH",
	"SMARTVPN_RUST_KERNEL_ADMISSION_FLOW",
	"SMARTVPN_RUST_KERNEL_FLOW",
	"SMARTVPN_RUST_LABELS_FLOW",
	"SMARTVPN_RUST_LIFECYCLE_FLOW",
	"SMARTVPN_RUST_MANUAL_FLOW",
	"SMARTVPN_RUST_METADATA_PROJECTION",
	"SMARTVPN_RUST_NODE_FRONT",
	"SMARTVPN_RUST_NODE_QUERY_FLOW",
	"SMARTVPN_RUST_PATH_FLOW",
	"SMARTVPN_RUST_PATH_MONITOR_FLOW",
	"SMARTVPN_RUST_POLICY_DNS",
	"SMARTVPN_RUST_PRIVACY_LOG",
	"SMARTVPN_RUST_PROFILE_FLOW",
	"SMARTVPN_RUST_QUOTA_FLOW",
	"SMARTVPN_RUST_REGION_FLOW",
	"SMARTVPN_RUST_REGION_JOB_FLOW",
	"SMARTVPN_RUST_SERVICE_STATE",
	"SMARTVPN_RUST_SETTINGS_FLOW",
	"SMARTVPN_RUST_SPEED_FLOW",
	"SMARTVPN_RUST_SUBSCRIPTION_FLOW",
	"SMARTVPN_RUST_SUBSCRIPTION_HTTP_FLOW",
	"SMARTVPN_RUST_TRAFFIC_FLOW",
	"SMARTVPN_RUST_TRAFFIC_PROJECTION",
	"SMARTVPN_RUST_TUN_REPORT",
}
var errNativeConfigurationFrozen = errors.New("native core configuration is frozen; configure before service startup and first native use")
var errNativeConfigurationInvalid = errors.New("native core requires an absolute UTF-8 library path without NUL")

func nativeConfigurationAllowed(state nativeCoreConfiguration, path string, enabled, active bool) error {
	if active || state.Configured || state.LoadStarted {
		return errNativeConfigurationFrozen
	}
	if (enabled || path != "") && !nativeLibraryPathAllowed(path) {
		return errNativeConfigurationInvalid
	}
	return nil
}
func nativeCoreSelectedPath(get func(string) string) string {
	if p := get("SMARTVPN_RUST_CORE_LIB"); p != "" {
		return p
	}
	return get("SMARTVPN_RUST_CORE_DLL")
}
func nativeCorePath() string {
	nativeConfiguration.Lock()
	defer nativeConfiguration.Unlock()
	nativeConfiguration.state.LoadStarted = true
	return nativeCoreSelectedPath(os.Getenv)
}

// Configuration is one-shot only on success; validation/environment failures may
// be retried. The environment adapter is injected in offline contract tests.
type nativeEnvironment interface {
	Lookup(string) (string, bool)
	Set(string, string) error
	Unset(string) error
}
type nativeOSEnvironment struct{}

func (nativeOSEnvironment) Lookup(k string) (string, bool) { return os.LookupEnv(k) }
func (nativeOSEnvironment) Set(k, v string) error          { return os.Setenv(k, v) }
func (nativeOSEnvironment) Unset(k string) error           { return os.Unsetenv(k) }
func nativeApplyConfiguration(state *nativeCoreConfiguration, path string, enabled, active bool, env nativeEnvironment) error {
	if err := nativeConfigurationAllowed(*state, path, enabled, active); err != nil {
		return err
	}
	value := "0"
	if enabled {
		value = "1"
	} else {
		path = ""
	}
	type previousValue struct {
		value   string
		present bool
	}
	previous := map[string]previousValue{}
	keys := append([]string{"SMARTVPN_RUST_CORE_DLL", "SMARTVPN_RUST_CORE_LIB"}, nativeCoreSwitches...)
	for _, key := range keys {
		v, ok := env.Lookup(key)
		previous[key] = previousValue{v, ok}
	}
	apply := func() error {
		for _, key := range nativeCoreSwitches {
			if err := env.Set(key, value); err != nil {
				return err
			}
		}
		if err := env.Set("SMARTVPN_RUST_CORE_DLL", ""); err != nil {
			return err
		}
		return env.Set("SMARTVPN_RUST_CORE_LIB", path)
	}
	if err := apply(); err != nil {
		for _, key := range keys {
			old := previous[key]
			if old.present {
				_ = env.Set(key, old.value)
			} else {
				_ = env.Unset(key)
			}
		}
		return err
	}
	state.Configured = true
	return nil
}

// This does not open a library or start a service, kernel or VPN connection.
func nativeConfigureCore(path string, enabled, active bool) error {
	nativeConfiguration.Lock()
	defer nativeConfiguration.Unlock()
	return nativeApplyConfiguration(&nativeConfiguration.state, path, enabled, active, nativeOSEnvironment{})
}
