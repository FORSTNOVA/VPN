package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

func privacyLogSecrets(names []string) ([]int, [][]byte) {
	indices := make([]int, 0, len(names))
	kept := make([][]byte, 0, len(names))
	seen := map[string]bool{}
	for i, s := range names {
		b, err := hex.DecodeString(s)
		if err != nil {
			panic("invalid secret codec")
		}
		b = bytes.TrimSpace(b)
		if len(b) < 12 || seen[string(b)] {
			continue
		}
		seen[string(b)] = true
		indices = append(indices, i)
		kept = append(kept, b)
	}
	return indices, kept
}
func runGoPrivacyLog(q privacyLogRequest, fx privacyLogEffects) privacyLogResult {
	a := privacyLogOracle{fx}
	if q.Op == "write" {
		r := a.call("write-source", "", "", 0)
		line := r.Bytes
		stripped := line
		for _, encoded := range r.Names {
			secret, err := hex.DecodeString(encoded)
			if err != nil {
				panic("invalid secret codec")
			}
			if bytes.Contains(stripped, secret) {
				stripped = redactLogBytes(stripped, secret)
			}
		}
		if err := a.call("sink", hex.EncodeToString(stripped), "", 0).Err; err != nil {
			return privacyLogResult{Err: err}
		}
		return privacyLogResult{Status: len(line)}
	}
	r := a.call("secrets", "", "", 0)
	indices, secrets := privacyLogSecrets(r.Names)
	if q.Op == "secrets" {
		return privacyLogResult{Data: map[string]any{"indices": indices}}
	}
	if q.Op != "scrub" {
		panic("invalid privacy operation")
	}
	r = a.call("read", "", "", 0)
	if r.Err != nil {
		return privacyLogResult{}
	}
	body := r.Bytes
	cleaned := tailFromLine(body, logScrubCap)
	dropped := body[:len(body)-len(cleaned)]
	redactedCount, droppedCount := 0, 0
	for _, secret := range secrets {
		droppedCount += bytes.Count(dropped, secret)
		if count := bytes.Count(cleaned, secret); count > 0 {
			redactedCount += count
			cleaned = redactLogBytes(cleaned, secret)
		}
	}
	if bytes.Equal(cleaned, body) {
		return privacyLogResult{}
	}
	if err := a.call("write-temp", hex.EncodeToString(cleaned), "", 0600).Err; err != nil {
		a.call("log-error", err.Error(), "", 0)
		return privacyLogResult{}
	}
	if err := a.call("rename", "", "", 0).Err; err != nil {
		a.call("log-error", err.Error(), "", 0)
		return privacyLogResult{}
	}
	note := ""
	if droppedCount > 0 {
		note = fmt.Sprintf(", and those held %d subscription URL(s)", droppedCount)
	}
	a.call("log-summary", fmt.Sprintf("kernel log: kept the newest %d kB, dropped %d kB of older lines%s, redacted %d subscription URL(s)", len(cleaned)/1024, len(dropped)/1024, note, redactedCount), "", 0)
	return privacyLogResult{}
}
