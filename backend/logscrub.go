package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
	stripped := line
	for _, secret := range secrets {
		if bytes.Contains(stripped, secret) {
			stripped = bytes.ReplaceAll(stripped, secret, []byte(redacted))
		}
	}
	if _, err := r.out.Write(stripped); err != nil {
		return 0, err
	}
	// The count is the caller's bytes, not the shortened line's: a writer reports
	// what it consumed, and log.Printf would treat anything less as a short write.
	return len(line), nil
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
	secrets := []string{a.settings.SubscriptionURL}
	for _, entry := range a.subscriptions {
		secrets = append(secrets, entry.URL)
	}
	// An empty or very short value is not a URL, and removing it would garble
	// unrelated text.
	kept := make([]string, 0, len(secrets))
	seen := map[string]bool{}
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 12 || seen[secret] {
			continue
		}
		seen[secret] = true
		kept = append(kept, secret)
	}
	return kept
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
	secrets := a.subscriptionSecrets()
	path := filepath.Join(a.home, "mihomo.log")
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cleaned := tailFromLine(body, logScrubCap)
	dropped := body[:len(body)-len(cleaned)]
	redactedCount, droppedCount := 0, 0
	for _, secret := range secrets {
		droppedCount += bytes.Count(dropped, []byte(secret))
		if count := bytes.Count(cleaned, []byte(secret)); count > 0 {
			redactedCount += count
			cleaned = bytes.ReplaceAll(cleaned, []byte(secret), []byte(redacted))
		}
	}
	if bytes.Equal(cleaned, body) {
		return
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, cleaned, 0600); err != nil {
		log.Printf("could not clean the kernel's log: %v", err)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		log.Printf("could not clean the kernel's log: %v", err)
		return
	}
	// Say which of the two removed the leak: a log that has been trimmed may no
	// longer hold a URL that was there, and a reader who sees "0 redacted" alone
	// would think nothing had been done.
	droppedNote := ""
	if droppedCount > 0 {
		droppedNote = fmt.Sprintf(", and those held %d subscription URL(s)", droppedCount)
	}
	log.Printf("kernel log: kept the newest %d kB, dropped %d kB of older lines%s, redacted %d subscription URL(s)",
		len(cleaned)/1024, len(dropped)/1024, droppedNote, redactedCount)
}

// tailFromLine returns the last limit bytes of body or more, always starting at
// the beginning of a line: it backs up to the last boundary at or before the
// window rather than beginning inside a line. A body with no boundary in it at
// all is truncated where the limit falls, which is the only thing left to do;
// real logs are lines.
func tailFromLine(body []byte, limit int) []byte {
	if len(body) <= limit {
		return body
	}
	start := len(body) - limit
	if cut := bytes.LastIndexByte(body[:start], '\n'); cut >= 0 {
		start = cut + 1
	}
	return body[start:]
}
