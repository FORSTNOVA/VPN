package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testURL  = "https://provider.example:1000/link/KVRGkAaacc4fMIfi?sub=3"
	otherURL = "https://other.example:2000/link/ZZZZZZZZZZZZ?sub=9"
)

func TestLogRedactorStripsTheSubscription(t *testing.T) {
	var out bytes.Buffer
	redactor := newLogRedactor(&out)
	redactor.Set([]string{testURL})

	line := "node switch failed for " + testURL + " (retrying)\n"
	written, err := redactor.Write([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	// log.Printf reports an error when a Write does not consume the whole line.
	if written != len(line) {
		t.Fatalf("Write reported %d of %d bytes", written, len(line))
	}
	if strings.Contains(out.String(), testURL) {
		t.Fatalf("the URL must not reach the log: %s", out.String())
	}
	if !strings.Contains(out.String(), redacted) {
		t.Fatalf("a removal must leave a mark: %s", out.String())
	}
	if !strings.Contains(out.String(), "node switch failed") || !strings.Contains(out.String(), "(retrying)") {
		t.Fatalf("the rest of the line must survive: %s", out.String())
	}

	// A line with nothing to strip is written through untouched.
	out.Reset()
	plain := "health sweep could not measure the group\n"
	if _, err := redactor.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if out.String() != plain {
		t.Fatalf("got %q, want %q", out.String(), plain)
	}
}

func TestLogRedactorFollowsTheSubscriptionInUse(t *testing.T) {
	var out bytes.Buffer
	redactor := newLogRedactor(&out)
	redactor.Set([]string{testURL})
	// Switching subscriptions makes the old URL no longer a secret of this
	// profile's log, and the new one a secret.
	redactor.Set([]string{otherURL})
	if _, err := redactor.Write([]byte(testURL + " and " + otherURL + "\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), otherURL) {
		t.Fatalf("the subscription in use must be stripped: %s", out.String())
	}
	if !strings.Contains(out.String(), testURL) {
		t.Fatalf("a URL that is no longer in use is not stripped: %s", out.String())
	}
}

func TestSubscriptionSecrets(t *testing.T) {
	a := &app{
		settings: settings{SubscriptionURL: testURL},
		subscriptions: []subscriptionEntry{
			{URL: testURL}, {URL: otherURL}, {URL: ""}, {URL: "http://a.b"}, {URL: otherURL},
		},
	}
	got := a.subscriptionSecrets()
	want := []string{testURL, otherURL}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v (deduplicated, and nothing too short to be a URL)", got, want)
	}
}

func writeTestLog(t *testing.T, home, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, name)
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// The kernel's log is where an earlier build put the subscription URL, token and
// all, and it is one of the files a user pastes into a bug report.
func TestScrubKernelLogRemovesTheSubscriptionURL(t *testing.T) {
	home := t.TempDir()
	path := writeTestLog(t, home, "mihomo.log", strings.Join([]string{
		`time="...1" level=info msg="Start initial configuration in progress"`,
		`time="...2" level=error msg="initial proxy provider SmartVPNSubscription error: Get \"` + testURL + `\": EOF"`,
		`time="...3" level=info msg="[TCP] 127.0.0.1:9000 --> www.youtube.com:443 match Match using SmartVPN"`,
		`time="...4" level=error msg="[Provider] SmartVPNSubscription pull error: Get \"` + testURL + `\": closed pipe"`,
		"",
	}, "\n"), 0600)

	a := &app{home: home, settings: settings{SubscriptionURL: testURL}}
	a.scrubKernelLog()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(testURL)) {
		t.Fatalf("the URL must be gone:\n%s", body)
	}
	if got := strings.Count(string(body), redacted); got != 2 {
		t.Fatalf("both occurrences must be marked, got %d:\n%s", got, body)
	}
	for _, kept := range []string{"Start initial configuration", "match Match using SmartVPN", "closed pipe"} {
		if !strings.Contains(string(body), kept) {
			t.Errorf("the rest of the log must survive, missing %q", kept)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary file must not be left behind")
	}
}

func TestScrubKernelLogLeavesACleanLogAlone(t *testing.T) {
	home := t.TempDir()
	body := "time=\"...1\" level=info msg=\"nothing to hide here\"\n"
	path := writeTestLog(t, home, "mihomo.log", body, 0600)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	a := &app{home: home, settings: settings{SubscriptionURL: testURL}}
	a.scrubKernelLog()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("a clean log must not be rewritten: %q", got)
	}
	// A rewrite replaces the file. The same file afterwards is what shows this
	// one was left exactly as it was, permissions and all.
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("a clean log must be left in place rather than replaced")
	}
}

func TestScrubKernelLogWithoutAFile(t *testing.T) {
	a := &app{home: filepath.Join(t.TempDir(), "missing"), settings: settings{SubscriptionURL: testURL}}
	a.scrubKernelLog()
}

func TestTailFromLine(t *testing.T) {
	body := []byte("first line\nsecond line\nthird line\n")
	// Under the limit everything is kept.
	if got := tailFromLine(body, len(body)); string(got) != string(body) {
		t.Fatalf("got %q, want the whole body", got)
	}
	// Over it, the kept part is a tail of whole lines, and backing up to the
	// boundary means it holds at least the limit asked for.
	got := tailFromLine(body, 20)
	if !bytes.HasSuffix(body, got) {
		t.Fatalf("the kept part must be a tail of the body: %q", got)
	}
	if len(got) < 20 {
		t.Fatalf("kept %d bytes, want at least the 20 asked for: %q", len(got), got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n") {
		if !strings.Contains(string(body), line+"\n") {
			t.Fatalf("a partial line survived: %q", line)
		}
	}
	// A window that lands inside the last line backs up to that line's start.
	ragged := []byte("short\n" + strings.Repeat("y", 40) + "\n")
	if got := string(tailFromLine(ragged, 20)); got != strings.Repeat("y", 40)+"\n" {
		t.Fatalf("got %q, want the whole last line", got)
	}
	// A body with no boundary at all can only be truncated where the limit falls.
	single := []byte("x" + strings.Repeat("y", 100))
	if got := tailFromLine(single, 10); len(got) != 10 {
		t.Fatalf("got %d bytes, want the limit", len(got))
	}
}
