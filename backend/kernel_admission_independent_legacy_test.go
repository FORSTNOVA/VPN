//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestKernelAdmissionIndependentLegacyLastLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{" \t\n ", ""}, {"first\n last \n", " last"}, {"\r\nA\r\nB\r\n", "B"}, {"a\rinside", "a\rinside"}, {"first\n\u2003X\u2003", "\u2003X"}} {
		if got := lastLine(tc.in); got != tc.want {
			t.Fatalf("last line %q want%q", got, tc.want)
		}
	}
	raw := strings.Repeat("a", 299) + "界"
	got := lastLine(raw)
	if !strings.HasPrefix(got, raw[:300]) || len(got) <= 300 || utf8.ValidString(got) {
		t.Fatalf("must truncate at byte 300 retaining partial rune: %x", got)
	}
	if got := lastLine(strings.Repeat("x", 300)); len(got) != 300 {
		t.Fatal("exact300 not truncated")
	}
}

func TestKernelAdmissionIndependentLegacyPaths(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	a := &app{home: home, bundleDir: bundle}
	fallback := filepath.Join(home, "mihomo.exe")
	if got := a.mihomoPath(); got != fallback {
		t.Fatalf("default %q", got)
	}
	bundled := filepath.Join(bundle, "mihomo.exe")
	if err := os.Mkdir(bundled, 0700); err != nil {
		t.Fatal(err)
	}
	if got := a.mihomoPath(); got != fallback {
		t.Fatal("directory must not be accepted as file")
	}
	if err := os.Remove(bundled); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("synthetic, not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := a.mihomoPath(); got != bundled {
		t.Fatalf("bundle %q", got)
	}
	testEditSettings(a, func(s *settings) { s.MihomoPath = "  missing-synthetic.exe \t" })
	if got := a.mihomoPath(); got != "missing-synthetic.exe" {
		t.Fatalf("saved path wins without stat %q", got)
	}
	testEditSettings(a, func(s *settings) { s.MihomoPath = " \t" })
	a.bundleDir = ""
	if got := a.mihomoPath(); got != fallback {
		t.Fatal("blank saved/bundle falls home")
	}
}

func TestKernelAdmissionIndependentLegacyExpiredReady(t *testing.T) {
	// The legacy pre-attempt deadline gate guarantees zero network operations here.
	// Positive timeouts are deliberately never passed to the real legacy adapter.
	a := &app{mixedPort: 0}
	for _, d := range []time.Duration{0, -time.Second} {
		if err := a.waitForMihomo(d); err == nil || err.Error() != "startup timeout" {
			t.Fatalf("expired deadline %v", err)
		}
	}
}
