package main

import (
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
)

type appPrivacyLogEffects struct {
	a           *app
	lastErr     error
	line        []byte
	rawSecrets  [][]byte
	keptStrings []string
	out         io.Writer
	readFile    func(string) ([]byte, error)
	writeFile   func(string, []byte, os.FileMode) error
	rename      func(string, string) error
	logf        func(string, ...any)
}

func (f *appPrivacyLogEffects) Release() {}
func (f *appPrivacyLogEffects) Call(e privacyLogEffect) privacyLogReply {
	switch e.Kind {
	case "secrets":
		raw := []string{f.a.controlMetaLocked().SubscriptionURL}
		for _, v := range f.a.catalogEntriesLocked() {
			raw = append(raw, v.URL)
		}
		f.keptStrings = make([]string, len(raw))
		names := make([]string, len(raw))
		for i, s := range raw {
			f.keptStrings[i] = s
			names[i] = hex.EncodeToString([]byte(f.keptStrings[i]))
		}
		return privacyLogReply{Names: names}
	case "write-source":
		names := make([]string, len(f.rawSecrets))
		for i, s := range f.rawSecrets {
			names[i] = hex.EncodeToString(s)
		}
		return privacyLogReply{Bytes: f.line, Names: names}
	case "read":
		read := f.readFile
		if read == nil {
			read = os.ReadFile
		}
		b, err := read(filepath.Join(f.a.home, "mihomo.log"))
		return privacyLogReply{Bytes: b, Err: err}
	case "write-temp":
		b, err := hex.DecodeString(e.Text)
		if err != nil {
			panic("invalid log payload")
		}
		write := f.writeFile
		if write == nil {
			write = os.WriteFile
		}
		f.lastErr = write(filepath.Join(f.a.home, "mihomo.log.tmp"), b, os.FileMode(e.Number))
		return privacyLogReply{Err: f.lastErr}
	case "rename":
		rename := f.rename
		if rename == nil {
			rename = os.Rename
		}
		f.lastErr = rename(filepath.Join(f.a.home, "mihomo.log.tmp"), filepath.Join(f.a.home, "mihomo.log"))
		return privacyLogReply{Err: f.lastErr}
	case "log-error", "log-summary":
		logf := f.logf
		if logf == nil {
			logf = log.Printf
		}
		if e.Kind == "log-error" {
			logf("could not clean the kernel's log: %v", f.lastErr)
		} else {
			logf("%s", e.Text)
		}
		return privacyLogReply{}
	case "sink":
		b, err := hex.DecodeString(e.Text)
		if err != nil {
			panic("invalid sink payload")
		}
		_, err = f.out.Write(b)
		return privacyLogReply{Err: err}
	}
	panic("unexpected privacy port")
}
