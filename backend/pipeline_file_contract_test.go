package main

import (
	"os"
	"path/filepath"
	"testing"
)

const pipelineContractYAML = "proxies:\n  - name: selected\n    type: ss\n    server: example.invalid\n    port: 443\n    cipher: aes-128-gcm\n    password: synthetic-only\n"

// These are real file-adapter contracts, confined to test-owned TempDir roots.
// No kernel, controller, store, secret provider, HTTP client, or timer is used.
func TestMigrationPipelineCacheInstallFiles(t *testing.T) {
	for _, mode := range []string{"success", "no-format", "format-write-fails", "source-write-fails", "nodes-rename-fails", "profile-rename-fails", "mkdir-fails", "profile-write-fails", "nodes-write-fails"} {
		t.Run(mode, func(t *testing.T) {
			a := testControlApp(testControlApp(&app{home: t.TempDir()}, func(a *app) {
				testCatalogWrite(a, []subscriptionEntry{{ID: "fixture", URL: "https://synthetic.invalid/sub", Label: "fixture-label"}})
			}), func(a *app) {
				testSetSettings(a, settings{SubscriptionURL: "https://synthetic.invalid/sub", SelectedNode: "selected"})
			})
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.MkdirAll(filepath.Dir(a.profileCachePath("fixture")), 0700))
			must(os.WriteFile(a.profileCachePath("fixture"), []byte(pipelineContractYAML), 0600))
			if mode != "no-format" {
				must(os.WriteFile(a.profileCachePath("fixture")+".format", []byte("synthetic-format"), 0600))
			}
			if mode == "mkdir-fails" {
				must(os.WriteFile(filepath.Dir(a.profilePath()), []byte("blocking-file"), 0600))
			} else {
				must(os.MkdirAll(filepath.Dir(a.profilePath()), 0700))
			}
			switch mode {
			case "profile-write-fails":
				must(os.Mkdir(a.profilePath()+".tmp", 0700))
			case "nodes-write-fails":
				must(os.Mkdir(a.cachedNodesPath()+".tmp", 0700))
			case "format-write-fails":
				must(os.Mkdir(a.profilePath()+".format", 0700))
			case "source-write-fails":
				must(os.Mkdir(a.profilePath()+".source", 0700))
			case "nodes-rename-fails":
				must(os.Mkdir(a.cachedNodesPath(), 0700))
			case "profile-rename-fails":
				must(os.Mkdir(a.profilePath(), 0700))
			}
			err := a.useCachedProfileLocked("fixture")
			wantErr := mode == "source-write-fails" || mode == "nodes-rename-fails" || mode == "profile-rename-fails" || mode == "mkdir-fails" || mode == "profile-write-fails" || mode == "nodes-write-fails"
			if (err != nil) != wantErr {
				t.Fatalf("error=%v wantError=%v", err, wantErr)
			}
			if mode == "mkdir-fails" || mode == "profile-write-fails" {
				if a.cachedNodes != nil {
					t.Fatal("failed install changed memory")
				}
				return
			}
			if mode == "profile-rename-fails" {
				if _, e := os.Stat(a.profilePath() + ".tmp"); !os.IsNotExist(e) {
					t.Fatal("failed profile rename left temp file")
				}
				return
			}
			body, e := os.ReadFile(a.profilePath())
			must(e)
			if string(body) != pipelineContractYAML {
				t.Fatal("profile bytes changed")
			}
			if mode == "source-write-fails" {
				if a.cachedNodes != nil {
					t.Fatal("source failure should precede node-memory update")
				}
				return
			}
			source, e := os.ReadFile(a.profilePath() + ".source")
			must(e)
			if string(source) != subscriptionHash(a.settingsLocked().SubscriptionURL) {
				t.Fatal("wrong profile owner hash")
			}
			if len(a.cachedNodes) != 1 || !a.cachedNodes[0].Selected || a.cachedNodes[0].SourceLabel != "fixture-label" {
				t.Fatalf("partial memory nodes lost: %+v", a.cachedNodes)
			}
			if mode == "nodes-rename-fails" {
				if _, e := os.Stat(a.cachedNodesPath() + ".tmp"); !os.IsNotExist(e) {
					t.Fatal("failed node rename left temp file")
				}
			}
		})
	}
}

func TestMigrationPipelineCacheInstallRejectsWithoutEffects(t *testing.T) {
	for _, mode := range []string{"missing", "empty-nodes", "unknown-id"} {
		t.Run(mode, func(t *testing.T) {
			a := testControlApp(&app{home: t.TempDir()}, func(a *app) { testSetSettings(a, settings{SubscriptionURL: "https://synthetic.invalid/sub"}) })
			if mode != "missing" {
				if err := os.MkdirAll(filepath.Dir(a.profileCachePath("fixture")), 0700); err != nil {
					t.Fatal(err)
				}
				body := pipelineContractYAML
				if mode == "empty-nodes" {
					body = "proxies: []\n"
				}
				if err := os.WriteFile(a.profileCachePath("fixture"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.useCachedProfileLocked("fixture"); err == nil {
				t.Fatal("invalid cache unexpectedly installed")
			}
			if _, err := os.Stat(a.profilePath()); !os.IsNotExist(err) {
				t.Fatal("rejected cache changed profile")
			}
		})
	}
}
