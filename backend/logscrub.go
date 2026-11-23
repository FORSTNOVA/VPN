package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// redacted is what a removed secret leaves behind, so a reader can see that
// something was there instead of wondering about a gap.
const redacted = "<redacted>"

// logRedactor strips known secrets from everything this service logs.
//
// The promise that a subscription URL never reaches SmartVPN's own log used to
// hold because every call site was written carefully. A redactor at the sink
// makes it hold because nothing can be written around it: a future log line that
// happens to carry the URL is stripped without anyone having to remember.
type logRedactor struct {
	out io.Writer

	mu      sync.Mutex
	secrets [][]byte
}

func newLogRedactor(out io.Writer) *logRedactor {
	return &logRedactor{out: out}
}

// Set replaces the set of secrets to strip. It is called when the settings load
// and whenever they change, because the subscription in use can.
func (r *logRedactor) Set(secrets []string) {
	next := make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		next = append(next, []byte(secret))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = next
}

func (r *logRedactor) Write(line []byte) (int, error) {
	r.mu.Lock()
	secrets := r.secrets
	r.mu.Unlock()
	fx := &appPrivacyLogEffects{line: line, rawSecrets: secrets, out: r.out}
	result := runSelectedPrivacyLog(privacyLogRequest{Op: "write"}, fx)
	return result.Status, result.Err
}

// logScrubCap bounds how much of the kernel's log is kept, in bytes. The kernel
// appends to it for as long as it runs and nothing rotates it, so it is trimmed
// to its newest part on every start, which is also what bounds the work the
// scrub below has to do.
const logScrubCap = 8 << 20

// subscriptionSecrets is every subscription URL this profile knows about: the
// active one and every saved one, because a log may have been written while a
// different subscription was in use.
func (a *app) subscriptionSecrets() []string {
	fx := &appPrivacyLogEffects{a: a}
	r := runSelectedPrivacyLog(privacyLogRequest{Op: "secrets"}, fx)
	indices := r.Data["indices"].([]int)
	out := make([]string, 0, len(indices))
	for _, i := range indices {
		out = append(out, strings.TrimSpace(fx.keptStrings[i]))
	}
	return out
}

// scrubKernelLog removes subscription URLs from the kernel's log and trims it to
// its newest part.
//
// The kernel fetches nothing itself: the service downloads the subscription and
// hands it to the kernel as a file, so no current build puts a URL anywhere the
// kernel could log. An earlier one did — it configured the kernel with the
// subscription URL as an HTTP provider — and every failed pull wrote the URL, its
// token and all, into mihomo.log, where it sits for good. That file is one a user
// pastes into a bug report, so each start cleans it. It runs before the kernel can
// be started, while nothing holds the file open: replacing a log that a live
// process still holds would send that process's later lines into the removed one.
func (a *app) scrubKernelLog() {
	fx := &appPrivacyLogEffects{a: a}
	runSelectedPrivacyLog(privacyLogRequest{Op: "scrub"}, fx)
}

// tailFromLine returns the last limit bytes of body or more, always starting at
// the beginning of a line: it backs up to the last boundary at or before the
// window rather than beginning inside a line. A body with no boundary in it at
// all is truncated where the limit falls, which is the only thing left to do;
// real logs are lines.
func tailFromLineGo(body []byte, limit int) []byte {
	if len(body) <= limit {
		return body
	}
	start := len(body) - limit
	if cut := bytes.LastIndexByte(body[:start], '\n'); cut >= 0 {
		start = cut + 1
	}
	return body[start:]
}
