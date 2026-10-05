//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// A copy of the app turns self-contained by carrying a marker beside the
// executable: everything the service writes then stays in that folder instead of
// the user's profile, so the whole thing can be moved, copied or deleted as one
// unit.
const (
	portableMarker  = "portable.txt"
	portableDataDir = "SmartVPN-data"
)

// portableHome reports the directory a self-contained copy keeps its data in. It
// is only offered when the marker is there and the directory can actually be
// written: a copy unpacked into a read-only place, or opened from inside an
// archive, has to fall back to the profile rather than fail to start.
func portableHome(exeDir string) (string, bool) {
	if exeDir == "" {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(exeDir, portableMarker)); err != nil {
		return "", false
	}
	home := filepath.Join(exeDir, portableDataDir)
	if err := os.MkdirAll(home, 0700); err != nil {
		log.Printf("this copy carries %s but %s cannot be created (%v); using the profile instead",
			portableMarker, home, err)
		return "", false
	}
	probe := filepath.Join(home, ".write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0600); err != nil {
		log.Printf("this copy carries %s but %s is not writable (%v); using the profile instead",
			portableMarker, home, err)
		return "", false
	}
	_ = os.Remove(probe)
	return home, true
}

// lookFor prefers a file that came with the package over one in the service's own
// directory. A portable copy therefore runs the kernel where it lies instead of
// duplicating sixty megabytes into its profile.
func (a *app) lookFor(name string) string {
	if a.bundleDir != "" {
		if bundled := filepath.Join(a.bundleDir, name); fileExists(bundled) {
			return bundled
		}
	}
	return filepath.Join(a.home, name)
}

// mihomoPath is the kernel the service will actually run: what the user chose,
// otherwise the copy that came with the package, otherwise the default location
// in the profile. It is resolved on every use rather than stored, so a folder
// that is moved keeps working.
func (a *app) mihomoPath() string {
	if saved := strings.TrimSpace(a.settings.MihomoPath); saved != "" {
		return saved
	}
	return a.lookFor("mihomo.exe")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
