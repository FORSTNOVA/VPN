package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// errTestStatus stands in for a Windows proxy setting that could not be read.
var errTestStatus = errors.New("the proxy setting could not be read")

// identifyServer is a copy of this app's service as another copy of this app
// finds it: an address answering with a name.
func identifyServer(t *testing.T, body string) (string, int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/identify" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host, value
}

func TestIdentifyAnswersWithoutAToken(t *testing.T) {
	a := &app{}
	recorder := httptest.NewRecorder()
	// No Authorization header: this is the one route that asks for none, so a
	// copy of this app can recognise another one from an address alone.
	a.identify(recorder, httptest.NewRequest(http.MethodGet, "/api/identify", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"app":"`+appName+`"`) {
		t.Fatalf("the answer must name the app: %s", body)
	}
	if !strings.Contains(body, `"connected":false`) {
		t.Fatalf("nothing is running, so nothing is connected: %s", body)
	}
}

func TestSmartVPNIsListeningAt(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"a copy of this app", `{"app":"` + appName + `","connected":true}`, true},
		{"an idle copy of this app", `{"app":"` + appName + `","connected":false}`, true},
		{"another proxy client", `{"hello":"clash"}`, false},
		{"an empty answer", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := identifyServer(t, tc.body)
			if got := smartVPNIsListeningAt(host); got != tc.want {
				t.Fatalf("smartVPNIsListeningAt = %v, want %v", got, tc.want)
			}
		})
	}

	if smartVPNIsListeningAt("127.0.0.1:1") {
		t.Fatal("a port nothing answers on must not count as another instance")
	}
	if smartVPNIsListeningAt("not an address") {
		t.Fatal("an address that cannot be parsed must not count as another instance")
	}
}

// The record is what lets a copy of this app tell whether the machine's one
// proxy setting is being served by another copy, and it lives in the registry
// the setting itself lives in.
func TestProxyOwnerRecordRoundTrip(t *testing.T) {
	original := proxyOwnerKeyPath
	proxyOwnerKeyPath = fmt.Sprintf(`Software\SmartVPN-test-%d`, os.Getpid())
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, proxyOwnerKeyPath)
		proxyOwnerKeyPath = original
	})

	if _, ok := readProxyOwner(); ok {
		t.Fatal("an unwritten record must not read as present")
	}
	owner := proxyOwner{Home: `C:\first`, APIPort: 40001, MixedPort: 40002}
	if err := writeProxyOwner(owner); err != nil {
		t.Skipf("this machine will not take a scratch key: %v", err)
	}
	got, ok := readProxyOwner()
	if !ok || got != owner {
		t.Fatalf("got %+v (%v), want %+v", got, ok, owner)
	}
	// The old record is replaced, not merged: the setting has one server.
	if err := writeProxyOwner(proxyOwner{Home: `C:\second`, APIPort: 1, MixedPort: 2}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readProxyOwner(); got.Home != `C:\second` {
		t.Fatalf("home = %q, want the newest writer", got.Home)
	}

	// A copy handing its own setting back must not erase another copy's claim.
	clearProxyOwner(`C:\first`)
	if _, ok := readProxyOwner(); !ok {
		t.Fatal("another copy's record must survive this copy's release")
	}
	clearProxyOwner(`C:\second`)
	if _, ok := readProxyOwner(); ok {
		t.Fatal("our own record must be cleared when the setting is handed back")
	}
	// Clearing again, with nothing there, stays harmless.
	clearProxyOwner(`C:\second`)
}

func TestDecodeProxyOwnerRejectsIncompleteRecords(t *testing.T) {
	cases := map[string]string{
		"garbage":      "not json",
		"no home":      `{"apiPort":1,"mixedPort":2}`,
		"no api port":  `{"home":"C:\\a","mixedPort":2}`,
		"no mixed":     `{"home":"C:\\a","apiPort":1}`,
		"wrong shape":  `[1,2,3]`,
		"empty string": ``,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeProxyOwner(text); ok {
				t.Fatalf("%q must not read as a record", text)
			}
		})
	}
	owner, ok := decodeProxyOwner(`{"home":"C:\\a","apiPort":1,"mixedPort":2}`)
	if !ok || owner.Home != `C:\a` || owner.APIPort != 1 || owner.MixedPort != 2 {
		t.Fatalf("a full record must be read: %+v %v", owner, ok)
	}
}

func TestProxyServedByAnotherInstance(t *testing.T) {
	otherHost, otherPort := identifyServer(t, `{"app":"`+appName+`","connected":true}`)

	cases := []struct {
		name  string
		state func() (bool, string, error)
		owner func() (proxyOwner, bool)
		self  string
		port  int
		want  bool
	}{
		{
			name:  "no proxy is set",
			state: func() (bool, string, error) { return false, "", nil },
			owner: func() (proxyOwner, bool) { return proxyOwner{}, false },
		},
		{
			name:  "another copy's record and the setting it describes",
			state: func() (bool, string, error) { return true, "127.0.0.1:5555", nil },
			owner: func() (proxyOwner, bool) {
				return proxyOwner{Home: `C:\other`, APIPort: otherPort, MixedPort: 5555}, true
			},
			want: true,
		},
		{
			name:  "the record names this copy",
			state: func() (bool, string, error) { return true, "127.0.0.1:5555", nil },
			owner: func() (proxyOwner, bool) {
				return proxyOwner{Home: `C:\mine`, APIPort: otherPort, MixedPort: 5555}, true
			},
			self: `C:\mine`,
		},
		{
			name:  "the setting has moved since the record was written",
			state: func() (bool, string, error) { return true, "127.0.0.1:6666", nil },
			owner: func() (proxyOwner, bool) {
				return proxyOwner{Home: `C:\other`, APIPort: otherPort, MixedPort: 5555}, true
			},
		},
		{
			name:  "the recorded owner is gone",
			state: func() (bool, string, error) { return true, "127.0.0.1:5555", nil },
			owner: func() (proxyOwner, bool) {
				return proxyOwner{Home: `C:\other`, APIPort: 1, MixedPort: 5555}, true
			},
		},
		{
			name:  "no record at all",
			state: func() (bool, string, error) { return true, "127.0.0.1:5555", nil },
			owner: func() (proxyOwner, bool) { return proxyOwner{}, false },
		},
		{
			name:  "the proxy address is empty",
			state: func() (bool, string, error) { return true, "", nil },
			owner: func() (proxyOwner, bool) {
				return proxyOwner{Home: `C:\other`, APIPort: otherPort, MixedPort: 5555}, true
			},
		},
		{
			name:  "the setting cannot be read",
			state: func() (bool, string, error) { return false, "", errTestStatus },
			owner: func() (proxyOwner, bool) {
				return proxyOwner{Home: `C:\other`, APIPort: otherPort, MixedPort: 5555}, true
			},
		},
		{
			name:  "another proxy client, which publishes no record",
			state: func() (bool, string, error) { return true, "127.0.0.1:7890", nil },
			owner: func() (proxyOwner, bool) { return proxyOwner{}, false },
		},
		{
			name:  "the test host itself",
			state: func() (bool, string, error) { return true, otherHost, nil },
			owner: func() (proxyOwner, bool) { return proxyOwner{}, false },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{home: tc.self, mixedPort: tc.port, proxyStateHook: tc.state, proxyOwnerHook: tc.owner}
			if got := a.proxyServedByAnotherInstance(); got != tc.want {
				t.Fatalf("proxyServedByAnotherInstance = %v, want %v", got, tc.want)
			}
		})
	}
}

// A machine has one system proxy setting. The copy already serving it keeps it,
// and the newcomer is told why instead of quietly taking it over.
func TestConnectRefusesWhileAnotherInstanceHoldsTheProxy(t *testing.T) {
	_, otherPort := identifyServer(t, `{"app":"`+appName+`","connected":true}`)
	a := &app{
		settings:       settings{ConnectionMode: modeSystemProxy},
		proxyStateHook: func() (bool, string, error) { return true, "127.0.0.1:5555", nil },
		proxyOwnerHook: func() (proxyOwner, bool) {
			return proxyOwner{Home: `C:\other`, APIPort: otherPort, MixedPort: 5555}, true
		},
	}
	recorder := httptest.NewRecorder()
	a.connect(recorder, httptest.NewRequest(http.MethodPost, "/api/connect", nil))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "另一个 SmartVPN") {
		t.Fatalf("the refusal must name the reason: %s", recorder.Body.String())
	}
	if a.kernel != nil {
		t.Fatal("no kernel may be started for a connection that is refused")
	}
}

// The same call without another copy in the way goes on to its own work, which
// here is the missing kernel — the point is that the guard did not fire.
func TestConnectProceedsWhenNobodyElseHoldsTheProxy(t *testing.T) {
	a := &app{
		home:     t.TempDir(),
		settings: settings{ConnectionMode: modeSystemProxy, MihomoPath: filepath.Join("no", "mihomo.exe")},
		proxyStateHook: func() (bool, string, error) { return true, "127.0.0.1:7890", nil },
		proxyOwnerHook: func() (proxyOwner, bool) { return proxyOwner{}, false },
	}
	recorder := httptest.NewRecorder()
	a.connect(recorder, httptest.NewRequest(http.MethodPost, "/api/connect", nil))

	if recorder.Code == http.StatusConflict {
		t.Fatalf("another client's setting must not be refused: %s", recorder.Body.String())
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want the missing-kernel failure: %s", recorder.Code, recorder.Body.String())
	}
}

// The chain end to end, with a real copy of this app's handler on the far side:
// the identity route's own answer is what the ownership probe recognises.
func TestTheIdentityRouteIsWhatTheProbeRecognises(t *testing.T) {
	other := &app{}
	server := httptest.NewServer(http.HandlerFunc(other.identify))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	a := &app{
		proxyStateHook: func() (bool, string, error) { return true, "127.0.0.1:5555", nil },
		proxyOwnerHook: func() (proxyOwner, bool) {
			return proxyOwner{Home: `C:\other`, APIPort: value, MixedPort: 5555}, true
		},
	}
	if !a.proxyServedByAnotherInstance() {
		t.Fatal("a copy answering on the recorded port must be recognised as another instance")
	}

	// And a copy does not read its own record as somebody else's.
	mine := &app{
		home:           `C:\other`,
		proxyStateHook: a.proxyStateHook,
		proxyOwnerHook: a.proxyOwnerHook,
	}
	if mine.proxyServedByAnotherInstance() {
		t.Fatal("an instance must not read its own record as another copy's")
	}
}

// The interface asks whether *our* proxy is in effect; the ownership guard asks
// whether the machine's proxy is on at all, whoever set it. Confusing the two is
// what made the guard miss a setting that another copy was serving, because the
// copy asking had not chosen a port of its own yet — at startup, and before the
// kernel is started at connect time.
func TestProxyStateIsRawWhileSystemProxyEnabledIsOurs(t *testing.T) {
	a := &app{
		mixedPort:      0,
		proxyStateHook: func() (bool, string, error) { return true, "127.0.0.1:4956", nil },
	}
	enabled, server, err := a.proxyState()
	if err != nil || !enabled || server != "127.0.0.1:4956" {
		t.Fatalf("the raw reading must report the setting: %v %q %v", enabled, server, err)
	}
	if ours, err := a.systemProxyEnabled(); err != nil || ours {
		t.Fatalf("a copy without a port of its own must not call the setting its own: %v %v", ours, err)
	}
	a.mixedPort = 4956
	if ours, err := a.systemProxyEnabled(); err != nil || !ours {
		t.Fatalf("the same setting is ours once the port matches: %v %v", ours, err)
	}
	a.mixedPort = 1234
	if ours, _ := a.systemProxyEnabled(); ours {
		t.Fatal("a setting pointing somewhere else is not ours")
	}
}
