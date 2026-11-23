//go:build windows

package main

import (
	"os"
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
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "portable"})
	e.bundleDir = exeDir
	r := runBootstrapWithEffects(e.q, e)
	return e.home, r.Err == nil && r.Bool
}

// lookFor prefers a file that came with the package over one in the service's own
// directory. A portable copy therefore runs the kernel where it lies instead of
// duplicating sixty megabytes into its profile.
func (a *app) lookFor(name string) string {
	r := a.runKernelAdmission(kernelAdmissionRequest{Op: "look-for", Name: name})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Text
}

// mihomoPath is the kernel the service will actually run: what the user chose,
// otherwise the copy that came with the package, otherwise the default location
// in the profile. It is resolved on every use rather than stored, so a folder
// that is moved keeps working.
func (a *app) mihomoPath() string {
	r := a.runKernelAdmission(kernelAdmissionRequest{Op: "path"})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Text
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
