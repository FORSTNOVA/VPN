package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivacyLogServiceTempCommitAndFailures(t *testing.T) {
	for _, mode := range []string{"ok", "write", "rename", "panic"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			a := settingsDocumentTestApp(t, home)
			secret := "https://synthetic.invalid/token"
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = secret })
			path := filepath.Join(home, "mihomo.log")
			original := []byte("prefix " + secret + "\n")
			if e := os.WriteFile(path, original, 0600); e != nil {
				t.Fatal(e)
			}
			fx := &appPrivacyLogEffects{a: a}
			var logs []string
			fx.logf = func(format string, v ...any) { logs = append(logs, fmt.Sprintf(format, v...)) }
			writes, renames := 0, 0
			fx.writeFile = func(p string, b []byte, m os.FileMode) error {
				writes++
				if p != path+".tmp" || m != 0600 {
					t.Fatal("temp/mode changed")
				}
				if mode == "write" || mode == "panic" {
					if e := os.WriteFile(p, []byte("partial"), m); e != nil {
						t.Fatal(e)
					}
					if mode == "panic" {
						panic("synthetic write")
					}
					return errors.New("synthetic write")
				}
				return os.WriteFile(p, b, m)
			}
			fx.rename = func(from, to string) error {
				renames++
				if mode == "rename" {
					return errors.New("synthetic rename")
				}
				return os.Rename(from, to)
			}
			var caught any
			func() {
				defer func() { caught = recover() }()
				runSelectedPrivacyLog(privacyLogRequest{Op: "scrub"}, fx)
			}()
			if (caught != nil) != (mode == "panic") || writes != 1 {
				t.Fatal("panic/replay")
			}
			got, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "ok" {
				if !bytes.Equal(got, []byte("prefix <redacted>\n")) || renames != 1 || len(logs) != 1 {
					t.Fatal("commit")
				}
				if _, e = os.Stat(path + ".tmp"); !os.IsNotExist(e) {
					t.Fatal("temp remains after success")
				}
			} else {
				if !bytes.Equal(got, original) {
					t.Fatal("original changed on failure")
				}
				if _, e = os.Stat(path + ".tmp"); e != nil {
					t.Fatal("old semantics retain partial temp")
				}
				if mode == "panic" && len(logs) != 0 {
					t.Fatal("panic caused later effects")
				}
			}
		})
	}
}
func TestPrivacyLogServiceRawBytesAndWriterCount(t *testing.T) {
	var out bytes.Buffer
	r := newLogRedactor(&out)
	r.Set([]string{string([]byte{'a', 255, 'b'}), ""})
	body := []byte{'x', 'a', 255, 'b', 0, 'z'}
	want := redactLogBytesGo(redactLogBytesGo(body, []byte{'a', 255, 'b'}), nil)
	n, e := r.Write(body)
	if e != nil || n != len(body) || !bytes.Equal(out.Bytes(), want) {
		t.Fatal("byte/rune or caller count changed")
	}
}

type privacyLogShortSink struct{ err error }

func (s privacyLogShortSink) Write([]byte) (int, error) { return 0, s.err }
func TestPrivacyLogServiceSinkOriginalShortWriteSemantics(t *testing.T) {
	for _, e := range []error{nil, io.ErrClosedPipe} {
		r := newLogRedactor(privacyLogShortSink{e})
		n, got := r.Write([]byte("abc"))
		if e == nil {
			if n != 3 || got != nil {
				t.Fatal("short nil error must be ignored")
			}
		} else if n != 0 || got != e {
			t.Fatal("error identity lost")
		}
	}
}
func TestPrivacyLogServiceTailBeforeOrderedReplacement(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	secret := "https://synthetic.invalid/secret"
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = secret })
	body := []byte(secret + "\n" + strings.Repeat("x", logScrubCap+17) + "\n" + secret)
	fx := &appPrivacyLogEffects{a: a, readFile: func(string) ([]byte, error) { return body, nil }}
	var written []byte
	var message string
	fx.writeFile = func(_ string, b []byte, _ os.FileMode) error { written = append([]byte(nil), b...); return nil }
	fx.rename = func(string, string) error { return nil }
	fx.logf = func(f string, v ...any) { message = fmt.Sprintf(f, v...) }
	runSelectedPrivacyLog(privacyLogRequest{Op: "scrub"}, fx)
	want := redactLogBytesGo(tailFromLineGo(body, logScrubCap), []byte(secret))
	if !bytes.Equal(written, want) || !strings.Contains(message, "those held 1 subscription URL(s)") || !strings.Contains(message, "redacted 1 subscription URL(s)") {
		t.Fatal("tail/count/replacement changed")
	}
}
func TestPrivacyLogServiceUnicodeTrimPreservesInvalidBytes(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	for _, raw := range []string{"\u0085\u00a0https://synthetic.invalid/" + string([]byte{255}) + "\u2003", string([]byte{255}) + "   https://synthetic.invalid/  ", "\x00https://synthetic.invalid/\u3000", " \u0085 short \u00a0 "} {
		testEditSettings(a, func(s *settings) { s.SubscriptionURL = raw })
		got := a.subscriptionSecrets()
		trimmed := strings.TrimSpace(raw)
		if len(trimmed) < 12 {
			if len(got) != 0 {
				t.Fatal("short filter changed")
			}
		} else if len(got) != 1 || !bytes.Equal([]byte(got[0]), []byte(trimmed)) {
			t.Fatal("lossy trim or byte length changed")
		}
	}
}
