package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortableHomeNeedsTheMarker(t *testing.T) {
	exeDir := t.TempDir()
	// Without the marker the copy behaves like an installed one.
	if home, ok := portableHome(exeDir); ok {
		t.Fatalf("a copy without the marker must not be portable, got %q", home)
	}
	if _, err := os.Stat(filepath.Join(exeDir, portableDataDir)); !os.IsNotExist(err) {
		t.Fatal("nothing may be created before the marker says so")
	}

	if err := os.WriteFile(filepath.Join(exeDir, portableMarker), []byte("portable"), 0600); err != nil {
		t.Fatal(err)
	}
	home, ok := portableHome(exeDir)
	if !ok {
		t.Fatal("a copy with the marker beside it is portable")
	}
	if home != filepath.Join(exeDir, portableDataDir) {
		t.Fatalf("home = %q, want the data folder beside the executable", home)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the data folder must be created: %v", err)
	}
	// The write probe is left behind by no run.
	if _, err := os.Stat(filepath.Join(home, ".write-probe")); !os.IsNotExist(err) {
		t.Fatal("the probe file must be cleaned up")
	}
}

func TestPortableHomeFallsBackWhenItCannotWrite(t *testing.T) {
	exeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(exeDir, portableMarker), []byte("portable"), 0600); err != nil {
		t.Fatal(err)
	}
	// A file where the data folder would go: the copy has to fall back to the
	// profile rather than refuse to start.
	if err := os.WriteFile(filepath.Join(exeDir, portableDataDir), []byte("not a folder"), 0600); err != nil {
		t.Fatal(err)
	}
	if home, ok := portableHome(exeDir); ok {
		t.Fatalf("an unusable data folder must fall back, got %q", home)
	}
	if _, ok := portableHome(""); ok {
		t.Fatal("an unknown executable directory is not portable")
	}
}

func TestLookForPrefersTheCopyThatCameWithThePackage(t *testing.T) {
	home := t.TempDir()
	bundle := t.TempDir()
	a := &app{home: home, bundleDir: bundle}

	// Nothing beside the executable: the service's own directory is used.
	if got := a.lookFor("mihomo.exe"); got != filepath.Join(home, "mihomo.exe") {
		t.Fatalf("got %q, want the profile", got)
	}

	if err := os.WriteFile(filepath.Join(bundle, "mihomo.exe"), []byte("kernel"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := a.lookFor("mihomo.exe"); got != filepath.Join(bundle, "mihomo.exe") {
		t.Fatalf("got %q, want the copy that came with the package", got)
	}
	// A directory is not a kernel.
	if err := os.MkdirAll(filepath.Join(bundle, "wintun.dll"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := a.lookFor("wintun.dll"); got != filepath.Join(home, "wintun.dll") {
		t.Fatalf("got %q, want the profile", got)
	}
}

func TestMihomoPathResolution(t *testing.T) {
	home := t.TempDir()
	bundle := t.TempDir()
	a := &app{home: home, bundleDir: bundle}

	// Nothing chosen and nothing bundled: the profile.
	if got := a.mihomoPath(); got != filepath.Join(home, "mihomo.exe") {
		t.Fatalf("got %q", got)
	}
	if err := os.WriteFile(filepath.Join(bundle, "mihomo.exe"), []byte("kernel"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := a.mihomoPath(); got != filepath.Join(bundle, "mihomo.exe") {
		t.Fatalf("the packaged kernel must win over the profile: %q", got)
	}
	// A path the user chose wins over everything, and is not rewritten.
	a.settings.MihomoPath = `D:\tools\mihomo.exe`
	if got := a.mihomoPath(); got != `D:\tools\mihomo.exe` {
		t.Fatalf("got %q, want the saved value", got)
	}
	a.settings.MihomoPath = "  "
	if got := a.mihomoPath(); got != filepath.Join(bundle, "mihomo.exe") {
		t.Fatalf("a blank value means 'not chosen': %q", got)
	}
}
