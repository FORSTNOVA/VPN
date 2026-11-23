//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type appDriverInstallEffects struct {
	a                     *app
	request               *http.Request
	content, archiveBytes []byte
	archive               *zip.Reader
	response              *http.Response
	entry                 io.ReadCloser
	owned                 bool
	readFile              func(string) ([]byte, error)
	writeFile             func(string, []byte, os.FileMode) error
	remove                func(string) error
	rename                func(string, string) error
	stat                  func(string) (os.FileInfo, error)
	hashFile              func(string) (string, error)
	signature             func(string) (string, error)
	get                   func(string) (*http.Response, error)
}

func newAppDriverInstallEffects(a *app, r *http.Request, q driverInstallRequest) *appDriverInstallEffects {
	e := &appDriverInstallEffects{a: a, request: r, content: q.Body, readFile: os.ReadFile, writeFile: os.WriteFile, remove: os.Remove, rename: os.Rename, stat: os.Stat, hashFile: fileSHA256, signature: runWintunSignatureProcess}
	e.get = func(address string) (*http.Response, error) {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client := http.Client{Timeout: 150 * time.Second, Transport: transport}
		return client.Get(address)
	}
	return e
}
func (e *appDriverInstallEffects) closeEntry() {
	if e.entry != nil {
		v := e.entry
		e.entry = nil
		_ = v.Close()
	}
}
func (e *appDriverInstallEffects) closeResponse() {
	if e.response != nil {
		v := e.response
		e.response = nil
		_ = v.Body.Close()
	}
}
func (e *appDriverInstallEffects) Release() {
	defer func() {
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	}()
	defer e.closeResponse()
	e.closeEntry()
}
func (e *appDriverInstallEffects) Call(q driverInstallEffect) (r driverInstallReply) {
	switch q.Kind {
	case "decode":
		var b struct {
			Path string `json:"path"`
		}
		if e.request != nil && e.request.Body != nil {
			r.Err = decodeJSON(e.request.Body, &b)
			r.Flag = errors.Is(r.Err, io.EOF)
		}
		r.Text = b.Path
	case "read-local":
		e.content, r.Err = e.readFile(q.Text)
	case "pe-header":
		r.Number = int64(len(e.content))
		n := len(e.content)
		if n > 64 {
			n = 64
		}
		r.Bytes = e.content[:n]
	case "pe-at":
		if q.Number < 0 || q.Number > int64(len(e.content))-6 {
			panic("invalid PE byte request")
		}
		r.Bytes = e.content[int(q.Number) : int(q.Number)+6]
	case "http-open":
		response, err := e.get(q.Text)
		r.Err = err
		if err == nil {
			e.response = response
			r.Number = int64(response.StatusCode)
		}
	case "http-read":
		e.archiveBytes, r.Err = io.ReadAll(io.LimitReader(e.response.Body, q.Number))
	case "http-close":
		e.closeResponse()
	case "hash-zip":
		h := sha256.Sum256(e.archiveBytes)
		r.Text = hex.EncodeToString(h[:])
	case "zip-open":
		e.archive, r.Err = zip.NewReader(bytes.NewReader(e.archiveBytes), int64(len(e.archiveBytes)))
		if r.Err == nil {
			for _, f := range e.archive.File {
				r.Names = append(r.Names, f.Name)
			}
		}
	case "zip-entry-open":
		if q.Number < 0 || q.Number >= int64(len(e.archive.File)) {
			panic("invalid zip entry")
		}
		entry, err := e.archive.File[q.Number].Open()
		r.Err = err
		if err == nil {
			e.entry = entry
		}
	case "zip-entry-read":
		e.content, r.Err = io.ReadAll(io.LimitReader(e.entry, q.Number))
	case "zip-entry-close":
		e.closeEntry()
	case "hash-dll":
		h := sha256.Sum256(e.content)
		r.Text = hex.EncodeToString(h[:])
	case "lock":
		if e.owned {
			panic("driver double lock")
		}
		e.a.mu.Lock()
		e.owned = true
	case "target":
		r.Text = e.a.wintunInstallPath()
	case "write-temp":
		r.Err = e.writeFile(q.Text, e.content, os.FileMode(q.Number))
	case "signature":
		r.Text, r.Err = e.signature(q.Text)
	case "remove-temp":
		r.Err = e.remove(q.Text)
	case "rename":
		r.Err = e.rename(q.Text, q.Other)
	case "log":
		log.Printf("installed wintun.dll at %s (signer %s)", q.Text, q.Other)
	case "stat":
		var info os.FileInfo
		info, r.Err = e.stat(q.Text)
		if r.Err == nil {
			r.Flag = info.IsDir()
		}
	case "hash-file":
		r.Text, r.Err = e.hashFile(q.Text)
	case "elevated":
		r.Flag = e.a.elevated
	default:
		panic("unknown driver install effect: " + q.Kind)
	}
	return
}
func runWintunSignatureProcess(path string) (string, error) {
	// The path is interpolated rather than passed through $args: an argument
	// after -Command does not reliably reach the script. No execution-policy
	// flag is needed either — that policy governs script files, not an inline
	// command, so asking for a bypass would request more than this uses.
	script := `$s = Get-AuthenticodeSignature -LiteralPath ` + psQuote(path) + `
if ($null -eq $s) { Write-Output 'none'; exit 0 }
if ($s.Status -ne 'Valid') { Write-Output ('invalid:' + $s.Status); exit 0 }
Write-Output ('valid:' + $s.SignerCertificate.Subject)`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
