package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// chinaListBody builds a body the size of a real address list, so the tests
// exercise the same path the country's own addresses take.
func chinaListBody(count int) []byte {
	var builder strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&builder, "10.%d.%d.0/24\n", i/256, i%256)
	}
	return []byte(builder.String())
}

func goodChinaFetch(body []byte) func() ([]byte, string, error) {
	return func() ([]byte, string, error) { return body, "https://example.test/china.txt", nil }
}

func TestParseChinaIPListNormalises(t *testing.T) {
	body := append(chinaListBody(1200), []byte(`
# a comment, and a blank line follows

1.0.1.0/24
1.0.1.0/24
1.0.2.0/23 # the same range written with a trailing note
2001:250::/30
`)...)
	ranges, err := parseChinaIPList(body)
	if err != nil {
		t.Fatal(err)
	}
	// 1200 generated ranges, one duplicate dropped, plus the IPv4 and the IPv6
	// range the country list also carries.
	if len(ranges) != 1203 {
		t.Fatalf("got %d ranges, want 1203", len(ranges))
	}
	if !sort.StringsAreSorted(ranges) {
		t.Fatal("the list handed to the kernel must be sorted")
	}
	seen := 0
	for _, entry := range ranges {
		if entry == "1.0.1.0/24" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("a repeated range must appear once, got %d", seen)
	}
	for _, want := range []string{"1.0.2.0/23", "2001:250::/30"} {
		if !containsRange(ranges, want) {
			t.Errorf("the list must keep %q", want)
		}
	}
}

func containsRange(ranges []string, want string) bool {
	for _, entry := range ranges {
		if entry == want {
			return true
		}
	}
	return false
}

func TestParseChinaIPListRefusesWhatIsNotAList(t *testing.T) {
	cases := map[string][]byte{
		"an error page":      []byte("<html><body>404 Not Found</body></html>"),
		"empty":              nil,
		"far too short":      chinaListBody(10),
		"a range that is not one": []byte("1.2.3.4/33\n" + string(chinaListBody(1200))),
		"a host instead of a network": []byte("not-even-an-address\n" + string(chinaListBody(1200))),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseChinaIPList(body); err == nil {
				t.Fatal("a body that is not an address list must be refused")
			}
		})
	}
}

func TestEnsureChinaIPListFetchesAndCaches(t *testing.T) {
	home := t.TempDir()
	status := ensureChinaIPList(home, goodChinaFetch(chinaListBody(1500)), chinaListIfOverdue)
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.Source == "" || status.UpdatedAt == "" {
		t.Fatalf("a fetched list must say where it came from: %+v", status)
	}
	entries, _, err := readChinaIPList(chinaIPListPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if entries != 1500 {
		t.Fatalf("the cache holds %d ranges, want 1500", entries)
	}
}

func TestEnsureChinaIPListDoesNotFetchWhatIsCurrent(t *testing.T) {
	home := t.TempDir()
	calls := 0
	fetch := func() ([]byte, string, error) {
		calls++
		return chinaListBody(1500), "https://example.test/china.txt", nil
	}
	ensureChinaIPList(home, fetch, chinaListIfOverdue)
	status := ensureChinaIPList(home, fetch, chinaListCached)
	if calls != 1 {
		t.Fatalf("a current list must not be fetched again: %d calls", calls)
	}
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("the cached list must still be reported: %+v", status)
	}
}

func TestEnsureChinaIPListRefreshesAnOverdueList(t *testing.T) {
	home := t.TempDir()
	calls := 0
	fetch := func() ([]byte, string, error) {
		calls++
		return chinaListBody(1500), "https://example.test/china.txt", nil
	}
	ensureChinaIPList(home, fetch, chinaListIfOverdue)

	old := time.Now().Add(-chinaIPListTTL - time.Hour)
	if err := os.Chtimes(chinaIPListPath(home), old, old); err != nil {
		t.Fatal(err)
	}
	// Nothing about a connection waits for this, so the overdue list is used as
	// it stands until a refresh is actually asked for.
	status := ensureChinaIPList(home, fetch, chinaListCached)
	if calls != 1 || !status.Available {
		t.Fatalf("an overdue list must still be used: %d calls, %+v", calls, status)
	}
	status = ensureChinaIPList(home, fetch, chinaListIfOverdue)
	if calls != 2 {
		t.Fatalf("an overdue list must be fetched again: %d calls", calls)
	}
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("unexpected status after a refresh: %+v", status)
	}
}

func TestEnsureChinaIPListKeepsTheLastGoodList(t *testing.T) {
	home := t.TempDir()
	ensureChinaIPList(home, goodChinaFetch(chinaListBody(1500)), chinaListIfOverdue)

	failing := func() ([]byte, string, error) { return nil, "", errors.New("no route to the source") }
	status := ensureChinaIPList(home, failing, chinaListNow)
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("a failed refresh must keep the list already there: %+v", status)
	}
	if status.Error == "" {
		t.Fatal("a failed refresh must still be reported")
	}
	entries, _, err := readChinaIPList(chinaIPListPath(home))
	if err != nil || entries != 1500 {
		t.Fatalf("the cache was disturbed by a failed refresh: %d %v", entries, err)
	}

	// A body that is not a list is refused on the way in as well, for the same
	// reason: the file on disk is what the kernel will read.
	status = ensureChinaIPList(home, func() ([]byte, string, error) {
		return []byte("<html>404</html>"), "https://example.test/china.txt", nil
	}, chinaListNow)
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("a refused body must keep the list already there: %+v", status)
	}
}

func TestEnsureChinaIPListWithNothingToFallBackOn(t *testing.T) {
	home := t.TempDir()
	status := ensureChinaIPList(home, func() ([]byte, string, error) {
		return nil, "", errors.New("no route to the source")
	}, chinaListIfOverdue)
	if status.Available {
		t.Fatalf("nothing was fetched and nothing was cached: %+v", status)
	}
	if status.Error == "" {
		t.Fatal("the reason must be reported")
	}
}

func TestEnsureChinaIPListTreatsADamagedCacheAsMissing(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(chinaIPListPath(home)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chinaIPListPath(home), []byte("half a fi"), 0600); err != nil {
		t.Fatal(err)
	}
	// Without a refresh the damaged file is simply not usable, which is what the
	// configuration generator needs to know.
	status := ensureChinaIPList(home, goodChinaFetch(chinaListBody(1500)), chinaListCached)
	if status.Available {
		t.Fatalf("a truncated cache must not be used: %+v", status)
	}
	status = ensureChinaIPList(home, goodChinaFetch(chinaListBody(1500)), chinaListIfOverdue)
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("a refresh must replace it: %+v", status)
	}
}

func TestProbeChinaRuleAcceptsTheInstalledKernel(t *testing.T) {
	kernel := filepath.Join(os.Getenv("APPDATA"), "SmartVPN", "mihomo.exe")
	if _, err := os.Stat(kernel); err != nil {
		t.Skip("no kernel installed to ask")
	}
	home := t.TempDir()
	a := &app{home: home, settings: settings{MihomoPath: kernel}}
	if !a.probeChinaRule() {
		t.Fatal("the installed kernel must accept the domestic address rules")
	}
	// The probe writes a configuration and a list of its own; leaving them
	// behind would put a stray file where the real configuration lives.
	if _, err := os.Stat(filepath.Join(home, "china-probe.yaml")); !os.IsNotExist(err) {
		t.Error("the probe must remove its configuration")
	}
	if _, err := os.Stat(filepath.Join(home, "providers", "china-probe.txt")); !os.IsNotExist(err) {
		t.Error("the probe must remove its list")
	}
	// A kernel that cannot be run is not a kernel that accepted the rules, and
	// neither is one that is not there at all: with no path chosen, the kernel
	// this profile would run is the one beside its data, which this profile has
	// none of.
	if empty := (&app{home: home}).probeChinaRule(); empty {
		t.Error("a profile with no kernel must not be reported as accepted")
	}
	missing := &app{home: home, settings: settings{MihomoPath: filepath.Join(home, "missing.exe")}}
	if missing.probeChinaRule() {
		t.Error("a missing kernel must not be reported as accepted")
	}
}

func waitForRefresh(t *testing.T, a *app) chinaIPStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		status := a.chinaIPStatusLocked()
		a.mu.Unlock()
		if !status.Refreshing {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the refresh never finished")
	return chinaIPStatus{}
}

func TestRefreshChinaDirectAnswersAtOnceAndReportsAfterwards(t *testing.T) {
	a := &app{home: t.TempDir()}
	started := make(chan struct{})
	a.chinaFetch = func() ([]byte, string, error) {
		close(started)
		return chinaListBody(1500), "https://example.test/china.txt", nil
	}
	recorder := httptest.NewRecorder()
	a.refreshChinaDirect(recorder, httptest.NewRequest(http.MethodPost, "/api/direct/refresh", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the page is told the work started, not made to wait: %d", recorder.Code)
	}
	<-started
	status := waitForRefresh(t, a)
	if !status.Available || status.Entries != 1500 {
		t.Fatalf("unexpected status: %+v", status)
	}
	// The kernel is not running, so the rule is ready rather than in effect.
	if status.Active {
		t.Error("a list fetched while nothing runs is not in effect")
	}
}

func TestStartChinaIPRefreshRefusesASecondRun(t *testing.T) {
	a := &app{home: t.TempDir()}
	release := make(chan struct{})
	a.chinaFetch = func() ([]byte, string, error) {
		<-release
		return chinaListBody(1500), "https://example.test/china.txt", nil
	}
	if !a.startChinaIPRefresh(chinaListNow) {
		t.Fatal("the first refresh must start")
	}
	if a.startChinaIPRefresh(chinaListNow) {
		t.Fatal("a second refresh must not start while one is running")
	}
	close(release)
	waitForRefresh(t, a)
}
