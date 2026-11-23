package main

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Domestic traffic has to stay domestic. A subscription's rule list names the
// services its author thought of; everything else — most of the country's
// sites, every CDN reached by a bare address, every app that talks to an IP —
// falls through to the last rule. While that last rule is the proxy, a
// domestic site is asked to answer an address abroad: pages that never load,
// logins that time out, and a symptom that reads as a broken line rather than
// as the routing mistake it is.
//
// The remedy is the one every desktop client ships: the country's own address
// ranges, matched before the catch-all. The list is fetched and cached here
// rather than by the kernel's own geo database, because mihomo refuses to
// start when a rule's database is missing and its default source is GitHub,
// which is not reachable from where this runs. A file we own degrades to "no
// rule at all" instead of "no connection".
const (
	chinaIPListFile  = "china-ip.txt"
	chinaIPListTTL   = 7 * 24 * time.Hour
	chinaIPListMax   = 4 << 20
	chinaIPMinRanges = 1000
	chinaIPFetchWait = 6 * time.Second
)

// The list is the addresses APNIC has delegated to operators in the country,
// aggregated and maintained in the open. jsdelivr serves the same files the
// repository does and is reachable from here where github.com is not; the raw
// host is kept as a last resort for a machine that can reach it.
var (
	chinaIPListFiles = []string{"china.txt", "china6.txt"}
	chinaIPMirrors   = []string{
		"https://cdn.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/",
		"https://testingcf.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/",
		"https://ghproxy.net/https://raw.githubusercontent.com/gaoyifan/china-operator-ip/ip-lists/",
	}
)

// chinaIPStatus is what the interface reports: whether the rules are in the
// configuration the kernel was started with, and where the list came from.
type chinaIPStatus struct {
	Available bool   `json:"available"`
	Entries   int    `json:"entries"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	Source    string `json:"source,omitempty"`
	Error     string `json:"error,omitempty"`
	// Set while a refresh is in flight, so the page can say so instead of
	// offering the same button again.
	Refreshing bool `json:"refreshing,omitempty"`
	// Whether the configuration the kernel is running actually carries the rule.
	// A list fetched after the kernel started is ready but not yet in effect.
	Active bool `json:"active,omitempty"`
}

func chinaIPListPath(home string) string {
	return filepath.Join(home, "providers", chinaIPListFile)
}

// parseChinaIPList validates an address list and returns it normalised: sorted,
// deduplicated and with comments and blank lines removed. The list is
// third-party input that becomes routing rules on the machine, so anything that
// is not a range is refused rather than passed on — and a body that is too
// short to be the country's own addresses is refused too, which is what catches
// an error page served in place of the file.
func parseChinaIPList(body []byte) ([]string, error) {
	r := (&app{}).runChinaService(chinaServiceRequest{Op: "parse", Body: body})
	return r.Ranges, r.Err
}

// downloadChinaIPList fetches every part of the list from the first mirror that
// serves all of them, and returns the merged body.
func downloadChinaIPList() ([]byte, string, error) {
	r := (&app{}).runChinaService(chinaServiceRequest{Op: "download"})
	return r.Body, r.Source, r.Err
}

func fetchChinaIPFile(url string) ([]byte, error) {
	// The list is fetched without any application-level proxy: it is needed
	// before the proxy is up, and it is served from a host that answers here.
	return fetchChinaIPFileUsing(url, newChinaIPHTTPClient())
}
func newChinaIPHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: chinaIPFetchWait, Transport: transport}
}
func fetchChinaIPFileUsing(url string, client *http.Client) ([]byte, error) {
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, chinaIPListMax+1))
	if err != nil {
		return nil, err
	}
	if len(body) > chinaIPListMax {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, chinaIPListMax)
	}
	return body, nil
}

// readChinaIPList reports what a cached list holds, so a file that was damaged
// or truncated is treated the same as one that was never fetched.
func readChinaIPList(path string) (int, time.Time, error) {
	e := newAppChinaEffects(&app{home: filepath.Dir(filepath.Dir(path))}, chinaServiceRequest{})
	e.path = path
	r := runSelectedChinaService(chinaServiceRequest{Op: "read"}, e)
	return r.Status.Entries, r.Now, r.Err
}

// chinaListMode is how much a caller is willing to do for the list. The three
// callers want three different things, and writing them as three names keeps
// each one honest: a connection must not wait for a download, a launch should
// keep the addresses current without downloading them every time, and the
// button means now.
type chinaListMode int

const (
	// chinaListCached reads what is on disk and nothing else.
	chinaListCached chinaListMode = iota
	// chinaListIfOverdue fetches a list that is missing, damaged or aged out.
	chinaListIfOverdue
	// chinaListNow fetches whatever is there.
	chinaListNow
)

// ensureChinaIPList makes sure a usable list is on disk and reports its state.
// A refresh that fails leaves the previous list in place: an out-of-date set of
// the country's own addresses still routes almost everything correctly, and it
// is always better than falling back to sending domestic traffic abroad.
func ensureChinaIPList(home string, fetch func() ([]byte, string, error), mode chinaListMode) chinaIPStatus {
	r := (&app{home: home}).runChinaService(chinaServiceRequest{Op: "ensure", Mode: mode, Fetch: fetch})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Status
}

func writeChinaIPList(path string, ranges []string) error {
	e := newAppChinaEffects(&app{home: filepath.Dir(filepath.Dir(path))}, chinaServiceRequest{})
	e.path = path
	r := runSelectedChinaService(chinaServiceRequest{Op: "write", Ranges: ranges}, e)
	return r.Err
}

// ---------------------------------------------------------------------------
// The service's copy of the list

// download is the list's source, or the fixture the tests put in its place.
func (a *app) downloadChinaIPList() ([]byte, string, error) {
	if a.chinaFetch != nil {
		return a.chinaFetch()
	}
	return downloadChinaIPList()
}

// chinaIPListStatus reads the cache and, when the mode allows, replaces it. It
// is called without the lock held: a refresh waits on the network, and holding
// the lock across that would stall every page that polls the service.
func (a *app) chinaIPListStatus(mode chinaListMode) chinaIPStatus {
	r := a.runChinaService(chinaServiceRequest{Op: "ensure", Mode: mode})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Status
}

// startChinaIPRefresh fetches in the background, so a connect that is waiting on
// the user is never held up by a download, and reports whether it started one.
func (a *app) startChinaIPRefresh(mode chinaListMode) bool {
	r := a.runChinaService(chinaServiceRequest{Op: "start-refresh", Mode: mode})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Flag
}

// chinaIPStatusLocked is what the pages read.
func (a *app) chinaIPStatusLocked() chinaIPStatus {
	r := a.runChinaService(chinaServiceRequest{Op: "status"})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Status
}

// refreshChinaDirect fetches the list again. It answers at once: the download
// runs in the background and the page reads the result from the state it polls,
// the same way the exit-region verification reports its progress.
func (a *app) refreshChinaDirect(w http.ResponseWriter, _ *http.Request) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer func() {
		if v := recover(); v != nil {
			if tracked.started {
				panic(v)
			}
			chinaHTTPFailure(w, nil)
		}
	}()
	a.startChinaIPRefresh(chinaListNow)
	writeJSON(tracked, map[string]any{"ok": true})
}

// chinaRuleProbe is the smallest configuration that uses the rules the domestic
// list is written with.
const chinaRuleProbe = `mixed-port: %d
mode: rule
log-level: warning
rule-providers:
  %s:
    type: file
    behavior: ipcidr
    format: text
    path: %s
rules:
  - RULE-SET,%s,DIRECT
  - MATCH,DIRECT
`

// chinaRuleSupported asks the kernel whether it accepts those rules, once per
// service. A rule-provider it cannot load makes the kernel refuse to start, and
// the alternative to asking here is finding out when the real configuration
// fails, which leaves the user with no connection at all rather than an
// imperfect one.
func (a *app) chinaRuleSupported() bool {
	r := a.runChinaService(chinaServiceRequest{Op: "supported"})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Flag
}

// probeChinaRule writes a configuration whose only job is to load the domestic
// address list, and asks the kernel whether it takes it. It is deliberately not
// the real configuration: this question is asked while that one is being
// built, and it must be answerable before the subscription's provider file is
// known to exist.
func (a *app) probeChinaRule() bool {
	r := a.runChinaService(chinaServiceRequest{Op: "probe"})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Flag
}

// lastLine keeps a kernel error report to something that fits on one log line.
func lastLine(output string) string {
	trimmed := strings.TrimSpace(output)
	if cut := strings.LastIndex(trimmed, "\n"); cut >= 0 {
		trimmed = trimmed[cut+1:]
	}
	if len(trimmed) > 300 {
		trimmed = trimmed[:300] + "…"
	}
	return trimmed
}
