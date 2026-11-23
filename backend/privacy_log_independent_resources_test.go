package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func privacyIndependentResources(t *testing.T, run privacyIndependentRunner) {
	for _, failure := range []string{"", "write", "rename"} {
		t.Run(failure, func(t *testing.T) {
			a := settingsDocumentTestApp(t, t.TempDir())
			secret := "abcdefghijkl"
			testSetSettings(a, settings{SubscriptionURL: "\u2003" + secret + "\u2003"})
			testCatalogWrite(a, []subscriptionEntry{{URL: secret + "long"}, {URL: secret}})
			path := filepath.Join(a.home, "mihomo.log")
			original := []byte(secret + "long\n")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			f := &appPrivacyLogEffects{a: a, logf: func(string, ...any) {}}
			if failure == "write" {
				f.writeFile = func(p string, b []byte, m os.FileMode) error {
					if m != 0600 {
						t.Fatal(m)
					}
					if err := os.WriteFile(p, b[:3], m); err != nil {
						t.Fatal(err)
					}
					return bootIndependentErr
				}
			}
			if failure == "rename" {
				f.rename = func(string, string) error { return bootIndependentErr }
			}
			r := run(privacyLogRequest{Op: "scrub"}, f)
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tmp, te := os.ReadFile(path + ".tmp")
			if failure == "" {
				if string(got) != "<redacted>long\n" || !os.IsNotExist(te) {
					t.Fatal("successful scrub")
				}
			} else {
				if !bytes.Equal(got, original) || te != nil {
					t.Fatal("failed scrub must keep original and partial temp")
				}
				if failure == "write" && string(tmp) != "<re" {
					t.Fatal("partial tmp changed")
				}
				if failure == "rename" && string(tmp) != "<redacted>long\n" {
					t.Fatal("complete tmp lost")
				}
			}
		})
	}
}
func TestPrivacyLogServiceIndependentResources(t *testing.T) {
	privacyIndependentResources(t, runSelectedPrivacyLog)
}
