package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	seen := map[string]bool{}
	ranges := make([]string, 0, 8192)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if cut := strings.IndexAny(line, " \t#"); cut >= 0 {
			line = line[:cut]
		}
		_, network, err := net.ParseCIDR(line)
		if err != nil {
			return nil, fmt.Errorf("not an address range: %q", line)
		}
		normalised := network.String()
		if seen[normalised] {
			continue
		}
		seen[normalised] = true
		ranges = append(ranges, normalised)
	}
	if len(ranges) < chinaIPMinRanges {
		return nil, fmt.Errorf("only %d address ranges, expected at least %d",
			len(ranges), chinaIPMinRanges)
	}
	sort.Strings(ranges)
	return ranges, nil
}

// downloadChinaIPList fetches every part of the list from the first mirror that
// serves all of them, and returns the merged body.
func downloadChinaIPList() ([]byte, string, error) {
	var lastErr error
	for _, mirror := range chinaIPMirrors {
		ranges := make([]string, 0, 16384)
		failed := false
		for _, name := range chinaIPListFiles {
			body, err := fetchChinaIPFile(mirror + name)
			if err != nil {
				lastErr = err
				failed = true
				break
			}
			part, err := parseChinaIPList(body)
			if err != nil {
				lastErr = fmt.Errorf("%s%s: %w", mirror, name, err)
				failed = true
				break
			}
			ranges = append(ranges, part...)
		}
		if failed || len(ranges) == 0 {
			continue
		}
		sort.Strings(ranges)
		return []byte(strings.Join(ranges, "\n") + "\n"), mirror, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no source for the address list answered")
	}
	return nil, "", lastErr
}

func fetchChinaIPFile(url string) ([]byte, error) {
	// The list is fetched without any application-level proxy: it is needed
	// before the proxy is up, and it is served from a host that answers here.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := http.Client{Timeout: chinaIPFetchWait, Transport: transport}
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
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, time.Time{}, err
	}
	ranges, err := parseChinaIPList(body)
	if err != nil {
		return 0, time.Time{}, err
	}
	return len(ranges), info.ModTime(), nil
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
	path := chinaIPListPath(home)
	entries, updated, err := readChinaIPList(path)
	cached := err == nil
	intact := func() chinaIPStatus {
		return chinaIPStatus{Available: true, Entries: entries, UpdatedAt: updated.Format(time.RFC3339)}
	}

	if mode == chinaListCached {
		if !cached {
			return chinaIPStatus{Available: false, Error: err.Error()}
		}
		return intact()
	}
	// A launch refreshes only what has aged out, which is what keeps it from
	// downloading the list on every start.
	if cached && mode == chinaListIfOverdue && time.Since(updated) <= chinaIPListTTL {
		return intact()
	}

	body, source, fetchErr := fetch()
	if fetchErr != nil {
		status := chinaIPStatus{Available: false, Error: fetchErr.Error()}
		if cached {
			status = intact()
			status.Error = "刷新失败，继续使用已有的列表：" + fetchErr.Error()
		}
		return status
	}
	ranges, parseErr := parseChinaIPList(body)
	if parseErr != nil {
		status := chinaIPStatus{Available: false, Error: parseErr.Error()}
		if cached {
			status = intact()
		}
		return status
	}
	if err := writeChinaIPList(path, ranges); err != nil {
		status := chinaIPStatus{Available: false, Error: err.Error()}
		if cached {
			status = intact()
			status.Error = "写入失败，继续使用已有的列表：" + err.Error()
		}
		return status
	}
	return chinaIPStatus{
		Available: true, Entries: len(ranges), Source: source,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
}

func writeChinaIPList(path string, ranges []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + ".tmp"
	body := strings.Join(ranges, "\n") + "\n"
	if err := os.WriteFile(temp, []byte(body), 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
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
	return ensureChinaIPList(a.home, a.downloadChinaIPList, mode)
}

// startChinaIPRefresh fetches in the background, so a connect that is waiting on
// the user is never held up by a download, and reports whether it started one.
func (a *app) startChinaIPRefresh(mode chinaListMode) bool {
	a.mu.Lock()
	if a.chinaFetching {
		a.mu.Unlock()
		return false
	}
	a.chinaFetching = true
	a.mu.Unlock()
	go func() {
		status := a.chinaIPListStatus(mode)
		a.mu.Lock()
		a.chinaFetching = false
		a.chinaDirect = status
		a.mu.Unlock()
		if status.Source != "" {
			log.Printf("domestic address list: %d ranges from %s", status.Entries, status.Source)
		} else if !status.Available {
			log.Printf("domestic traffic will be carried by the proxy: %s", status.Error)
		}
	}()
	return true
}

// chinaIPStatusLocked is what the pages read.
func (a *app) chinaIPStatusLocked() chinaIPStatus {
	status := a.chinaDirect
	status.Refreshing = a.chinaFetching
	status.Active = a.chinaDirectActive
	return status
}

// refreshChinaDirect fetches the list again. It answers at once: the download
// runs in the background and the page reads the result from the state it polls,
// the same way the exit-region verification reports its progress.
func (a *app) refreshChinaDirect(w http.ResponseWriter, _ *http.Request) {
	a.startChinaIPRefresh(chinaListNow)
	writeJSON(w, map[string]any{"ok": true})
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
	if a.chinaProbed {
		return a.chinaRuleOK
	}
	a.chinaProbed = true
	a.chinaRuleOK = a.probeChinaRule()
	return a.chinaRuleOK
}

// probeChinaRule writes a configuration whose only job is to load the domestic
// address list, and asks the kernel whether it takes it. It is deliberately not
// the real configuration: this question is asked while that one is being
// built, and it must be answerable before the subscription's provider file is
// known to exist.
func (a *app) probeChinaRule() bool {
	port, err := freeLoopbackPort()
	if err != nil {
		return false
	}
	dir := filepath.Join(a.home, "providers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false
	}
	listName := "china-probe.txt"
	configPath := filepath.Join(a.home, "china-probe.yaml")
	defer func() {
		_ = os.Remove(filepath.Join(dir, listName))
		_ = os.Remove(configPath)
	}()
	if err := os.WriteFile(filepath.Join(dir, listName), []byte("1.0.1.0/24\n223.255.252.0/23\n"), 0600); err != nil {
		return false
	}
	config := fmt.Sprintf(chinaRuleProbe, port, chinaProviderName,
		filepath.Join("providers", listName), chinaProviderName)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return false
	}
	if err := a.checkKernelConfig(configPath); err != nil {
		log.Printf("the kernel did not accept the domestic address rules, connecting without them: %v", err)
		return false
	}
	return true
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
