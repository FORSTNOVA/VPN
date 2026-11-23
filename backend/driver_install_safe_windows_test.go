//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDriverInstallSafeTempFilesAndFailureCleanup(t *testing.T) {
	for _, failure := range []string{"", "write", "signature", "rename"} {
		t.Run(failure, func(t *testing.T) {
			initial := t.TempDir()
			targetHome := t.TempDir()
			source := filepath.Join(t.TempDir(), "synthetic.bin")
			body := driverIndependentPE(0x8664)
			if e := os.WriteFile(source, body, 0600); e != nil {
				t.Fatal(e)
			}
			a := settingsDocumentTestApp(t, initial)
			encoded, _ := json.Marshal(map[string]string{"path": "  " + source + "  "})
			q := driverInstallRequest{Op: "install"}
			fx := newAppDriverInstallEffects(a, httptest.NewRequest("POST", "/driver", bytes.NewReader(encoded)), q)
			fx.get = func(string) (*http.Response, error) { t.Fatal("local branch attempted HTTP"); return nil, nil }
			fx.readFile = func(p string) ([]byte, error) {
				if !a.mu.TryLock() {
					t.Fatal("source read held app lock")
				}
				a.home = targetHome
				a.mu.Unlock()
				return os.ReadFile(p)
			}
			fx.writeFile = func(p string, b []byte, m os.FileMode) error {
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("write outside lock")
				}
				if m != 0600 {
					t.Fatal("wrong mode")
				}
				if failure == "write" {
					_ = os.WriteFile(p, []byte("partial"), m)
					return errors.New("synthetic write")
				}
				return os.WriteFile(p, b, m)
			}
			fx.signature = func(path string) (string, error) {
				if a.mu.TryLock() {
					a.mu.Unlock()
					t.Fatal("signature outside lock")
				}
				if path != filepath.Join(targetHome, "wintun.dll.tmp") {
					t.Fatal("target captured before lock")
				}
				if failure == "signature" {
					return "invalid:synthetic", nil
				}
				return " valid:CN=WireGuard LLC ", nil
			}
			if failure == "rename" {
				fx.rename = func(string, string) error { return errors.New("synthetic rename") }
			}
			out := runDriverInstallWithEffects(q, fx)
			target := filepath.Join(targetHome, "wintun.dll")
			tmp := target + ".tmp"
			if failure == "" {
				if out.Err != nil || out.Status != 200 {
					t.Fatal(out)
				}
				got, e := os.ReadFile(target)
				if e != nil || !bytes.Equal(got, body) {
					t.Fatal("temp rename lost content")
				}
				if out.Data["available"] != false {
					t.Fatal("unprivileged successful install was rejected or marked available")
				}
			} else {
				if out.Err == nil {
					t.Fatal("missing synthetic failure")
				}
				_, e := os.Stat(tmp)
				if failure == "write" {
					if e != nil {
						t.Fatal("write failure removed partial temp")
					}
				} else if !os.IsNotExist(e) {
					t.Fatal("signature/rename failure did not remove temp")
				}
			}
			if !a.mu.TryLock() {
				t.Fatal("mutex leaked")
			}
			a.mu.Unlock()
		})
	}
}

type driverSafeReadCloser struct {
	io.Reader
	closeFn func()
}

func (r driverSafeReadCloser) Close() error { r.closeFn(); return nil }

type driverSafeArchiveFX struct {
	base  *appDriverInstallEffects
	trace []string
}

func (f *driverSafeArchiveFX) Call(q driverInstallEffect) driverInstallReply {
	f.trace = append(f.trace, q.Kind)
	switch q.Kind {
	case "hash-zip":
		return driverInstallReply{Text: driverWintunZipSHA256}
	case "hash-dll":
		return driverInstallReply{Text: driverWintunDLLSHA256}
	}
	return f.base.Call(q)
}
func (f *driverSafeArchiveFX) Release() { f.base.Release() }
func TestDriverInstallSafeFakeHTTPRealArchiveClosesInOrder(t *testing.T) {
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, e := z.Create(driverWintunEntry)
	if e != nil {
		t.Fatal(e)
	}
	payload := append(driverIndependentPE(0x8664), bytes.Repeat([]byte{0}, 2*1024*1024)...)
	_, _ = f.Write(payload)
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	q := driverInstallRequest{Op: "download"}
	base := newAppDriverInstallEffects(nil, nil, q)
	closed := 0
	base.get = func(address string) (*http.Response, error) {
		if address != driverWintunURL {
			t.Fatal("wrong URL")
		}
		return &http.Response{StatusCode: 200, Body: driverSafeReadCloser{Reader: bytes.NewReader(archive.Bytes()), closeFn: func() { closed++ }}}, nil
	}
	fx := &driverSafeArchiveFX{base: base}
	out := runDriverInstallWithEffects(q, fx)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(base.content) != 2*1024*1024 {
		t.Fatal("DLL read cap changed")
	}
	if closed != 1 {
		t.Fatal("HTTP response not closed once")
	}
	n := len(fx.trace)
	if n < 2 || !reflect.DeepEqual(fx.trace[n-2:], []string{"zip-entry-close", "http-close"}) {
		t.Fatal("close order changed")
	}
	if base.entry != nil || base.response != nil {
		t.Fatal("resource ownership leaked")
	}
}
func TestDriverInstallSafeHTTPBoundAndHashPriority(t *testing.T) {
	q := driverInstallRequest{Op: "download"}
	fx := newAppDriverInstallEffects(nil, nil, q)
	closed := 0
	fx.get = func(string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: driverSafeReadCloser{Reader: strings.NewReader(strings.Repeat("x", 8*1024*1024+1)), closeFn: func() { closed++ }}}, nil
	}
	out := runDriverInstallWithEffects(q, fx)
	if !errors.Is(out.Err, driverBadWintun) || len(fx.archiveBytes) != 8*1024*1024 || fx.archive != nil || closed != 1 {
		t.Fatal("HTTP cap/hash before archive/close changed")
	}
}

type driverOwnedFX struct {
	base     *appDriverInstallEffects
	releases int
}

func (f *driverOwnedFX) Call(e driverInstallEffect) driverInstallReply { return f.base.Call(e) }
func (f *driverOwnedFX) Release()                                      { f.releases++; f.base.Release() }

type driverOwnedWriter struct {
	*httptest.ResponseRecorder
	a      *app
	t      *testing.T
	crash  bool
	writes int
}

func (w *driverOwnedWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("driver response outside real app lock")
	}
	if w.crash {
		panic("synthetic driver writer")
	}
	return w.ResponseRecorder.Write(b)
}
func (w *driverOwnedWriter) WriteHeader(n int) {
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("driver header outside real app lock")
	}
	w.ResponseRecorder.WriteHeader(n)
}
func TestDriverInstallOwnedHTTPResponseThenUnlock(t *testing.T) {
	for _, mode := range []string{"success", "signaturefail", "writerpanic"} {
		t.Run(mode, func(t *testing.T) {
			a := settingsDocumentTestApp(t, t.TempDir())
			source := filepath.Join(t.TempDir(), "source.bin")
			if err := os.WriteFile(source, driverIndependentPE(0x8664), 0600); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"path": source})
			q := driverInstallRequest{Op: "install"}
			base := newAppDriverInstallEffects(a, httptest.NewRequest("POST", "/driver", bytes.NewReader(body)), q)
			base.signature = func(string) (string, error) {
				if mode == "signaturefail" {
					return "invalid:test", nil
				}
				return "valid:WireGuard LLC", nil
			}
			fx := &driverOwnedFX{base: base}
			w := &driverOwnedWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t, crash: mode == "writerpanic"}
			var p any
			func() { defer func() { p = recover() }(); runDriverInstallHTTPWithEffects(w, q, fx) }()
			if (p != nil) != (mode == "writerpanic") {
				t.Fatal("writer panic altered")
			}
			if fx.releases != 1 || base.owned {
				t.Fatal("release not exact once")
			}
			if !a.mu.TryLock() {
				t.Fatal("real mutex leaked")
			}
			a.mu.Unlock()
			if mode == "signaturefail" && w.Code != 502 {
				t.Fatal("signature failure status")
			}
			if mode == "writerpanic" && w.writes != 1 {
				t.Fatal("response replayed")
			}
		})
	}
}
