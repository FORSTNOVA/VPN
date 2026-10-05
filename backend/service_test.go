package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeService(t *testing.T, token string, elevated bool) (serviceRecord, *int) {
	t.Helper()
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/state" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if elevated {
			_, _ = w.Write([]byte(`{"connected":true,"elevated":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"connected":false,"elevated":false}`))
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return serviceRecord{PID: os.Getpid(), Port: port, Token: token}, &hits
}

func TestRunningServiceWithNoRecord(t *testing.T) {
	home := t.TempDir()
	if _, alive := runningService(home); alive {
		t.Fatal("no record means no running service")
	}
}

func TestRunningServiceIgnoresAStaleRecord(t *testing.T) {
	home := t.TempDir()
	// A record left behind by a crash points at a port nothing listens on.
	if err := writeServiceRecord(home, serviceRecord{PID: 1, Port: 1, Token: "gone"}); err != nil {
		t.Fatal(err)
	}
	if _, alive := runningService(home); alive {
		t.Fatal("a record whose port no longer answers must be treated as dead")
	}
}

func TestRunningServiceRejectsAWrongToken(t *testing.T) {
	home := t.TempDir()
	record, _ := fakeService(t, "the-right-token", false)
	if err := writeServiceRecord(home, serviceRecord{PID: record.PID, Port: record.Port, Token: "the-wrong-token"}); err != nil {
		t.Fatal(err)
	}
	// An unrelated process holding the port must not be mistaken for our service.
	if _, alive := runningService(home); alive {
		t.Fatal("a service that rejects the token must not count as ours")
	}
}

func TestRunningServiceFindsTheLiveOne(t *testing.T) {
	home := t.TempDir()
	record, _ := fakeService(t, "token", true)
	if err := writeServiceRecord(home, record); err != nil {
		t.Fatal(err)
	}
	info, alive := runningService(home)
	if !alive {
		t.Fatal("the live service should have been found")
	}
	if info.Record.Port != record.Port || info.Record.Token != "token" {
		t.Fatalf("unexpected record: %+v", info.Record)
	}
	if !info.Elevated {
		t.Fatal("the reported privilege level decides whether a relaunch takes over")
	}
}

func TestRunningServiceRejectsAMalformedRecord(t *testing.T) {
	home := t.TempDir()
	for name, body := range map[string]string{
		"garbage":     "not json",
		"no port":     `{"pid":1,"token":"x"}`,
		"no token":    `{"pid":1,"port":9}`,
		"empty":       ``,
		"wrong shape": `[1,2,3]`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(serviceRecordPath(home), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, alive := runningService(home); alive {
				t.Fatalf("%q must not be read as a live service", body)
			}
		})
	}
}

func TestServiceRecordRoundTrip(t *testing.T) {
	home := t.TempDir()
	record := serviceRecord{PID: os.Getpid(), Port: 4321, Token: "secret"}
	if err := writeServiceRecord(home, record); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(serviceRecordPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var loaded serviceRecord
	if err := json.Unmarshal(body, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded != record {
		t.Fatalf("got %+v, want %+v", loaded, record)
	}

	// A different process must not be able to delete our address.
	removeServiceRecord(home, record.PID+1)
	if _, err := os.Stat(serviceRecordPath(home)); err != nil {
		t.Fatal("another pid must not remove the live record")
	}
	removeServiceRecord(home, record.PID)
	if _, err := os.Stat(serviceRecordPath(home)); !os.IsNotExist(err) {
		t.Fatal("our own shutdown should remove the record")
	}
	// Removing again, or without a file, must stay harmless.
	removeServiceRecord(home, record.PID)
	removeServiceRecord(filepath.Join(home, "missing"), record.PID)
}

func TestStaleDetection(t *testing.T) {
	size, mtime := selfIdentity()
	if size == 0 {
		t.Skip("the test binary's own identity is unavailable")
	}
	current := runningServiceInfo{Record: serviceRecord{ExeSize: size, ExeMtime: mtime}}
	if current.stale() {
		t.Fatal("a record written by this binary must not read as stale")
	}
	rebuilt := runningServiceInfo{Record: serviceRecord{ExeSize: size + 1}}
	if !rebuilt.stale() {
		t.Fatal("a changed size means the binary was rebuilt")
	}
	// A record from a build that published no identity stays usable.
	legacy := runningServiceInfo{Record: serviceRecord{Port: 1, Token: "x"}}
	if legacy.stale() {
		t.Fatal("a record without an identity must not be called stale")
	}
}

func TestAdoptBundledFiles(t *testing.T) {
	home := t.TempDir()
	bundle := t.TempDir()
	for name, body := range map[string]string{"mihomo.exe": "kernel", "wintun.dll": "library"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A file the user already has is never replaced.
	if err := os.WriteFile(filepath.Join(home, "mihomo.exe"), []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}

	a := &app{home: home}
	a.adoptBundledFiles(bundle)

	kept, err := os.ReadFile(filepath.Join(home, "mihomo.exe"))
	if err != nil || string(kept) != "mine" {
		t.Fatalf("an existing kernel must be kept: %q %v", kept, err)
	}
	copied, err := os.ReadFile(filepath.Join(home, "wintun.dll"))
	if err != nil || string(copied) != "library" {
		t.Fatalf("the bundled library must be adopted: %q %v", copied, err)
	}

	// A folder with nothing to offer, or no folder at all, is not an error.
	a.adoptBundledFiles(t.TempDir())
	a.adoptBundledFiles("")
}

func TestShouldTakeOver(t *testing.T) {
	cases := []struct {
		name     string
		existing runningServiceInfo
		elevated bool
		want     bool
	}{
		{
			name:     "same privilege and current build is reused",
			existing: runningServiceInfo{},
			want:     false,
		},
		{
			name:     "an elevated service is never replaced by an unelevated one",
			existing: runningServiceInfo{Elevated: true},
			elevated: false,
			want:     false,
		},
		{
			name:     "relaunching as administrator takes over an unelevated service",
			existing: runningServiceInfo{Elevated: false},
			elevated: true,
			want:     true,
		},
		{
			name:     "an idle service from a rebuilt binary is replaced",
			existing: runningServiceInfo{Record: serviceRecord{ExeSize: -1}},
			want:     true,
		},
		{
			name: "a stale service is replaced even when it outranks us",
			existing: runningServiceInfo{
				Elevated: true,
				Record:   serviceRecord{ExeSize: -1},
			},
			elevated: false,
			want:     true,
		},
		{
			name: "a busy service from a rebuilt binary is left alone",
			existing: runningServiceInfo{
				Record:    serviceRecord{ExeSize: -1},
				Connected: true,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldTakeOver(tc.existing, tc.elevated); got != tc.want {
				t.Fatalf("shouldTakeOver = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakePeer stands in for a service an earlier launch left running: it answers
// the probe until it is asked to shut down, and it can be made to keep serving
// after that request, which is what a wedged service looks like.
type fakePeer struct {
	token    string
	elevated bool

	mu      sync.Mutex
	alive   bool
	asked   bool
	ignores bool
	// The status the shutdown request is answered with; zero means 200.
	stopStatus int
}

func (p *fakePeer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+p.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/shutdown":
		p.asked = true
		if !p.ignores {
			// The real service closes its listener; a status the probe does not
			// accept stands in for a listener that is no longer there.
			p.alive = false
		}
		status := p.stopStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	case r.Method == http.MethodGet && r.URL.Path == "/api/state":
		if !p.alive {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"connected":false,"elevated":` +
			strconv.FormatBool(p.elevated) + `}`))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// willNotStop makes the peer keep serving after being asked to shut down,
// answering the request with status.
func (p *fakePeer) willNotStop(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ignores = true
	p.stopStatus = status
}

func (p *fakePeer) wasAskedToStop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked
}

func fakeServicePeer(t *testing.T, token string) (serviceRecord, *fakePeer) {
	t.Helper()
	peer := &fakePeer{token: token, alive: true}
	server := httptest.NewServer(http.HandlerFunc(peer.handle))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return serviceRecord{PID: os.Getpid(), Port: port, Token: token}, peer
}

// livePeer publishes a record for a peer that is serving, which is what a launch
// finds when a service from an earlier launch is still up.
func livePeer(t *testing.T, home, token string) (serviceRecord, *fakePeer) {
	t.Helper()
	record, peer := fakeServicePeer(t, token)
	if err := writeServiceRecord(home, record); err != nil {
		t.Fatal(err)
	}
	return record, peer
}

func TestHandOverToWaitsForTheServiceToStop(t *testing.T) {
	record, peer := fakeServicePeer(t, "token")
	if !handOverTo(runningServiceInfo{Record: record}, 2*time.Second) {
		t.Fatal("a service that stopped answering must be reported as gone")
	}
	if !peer.wasAskedToStop() {
		t.Fatal("the service must be asked to shut down")
	}
}

func TestHandOverToReportsAServiceThatWillNotStop(t *testing.T) {
	// The status the request is answered with is not the verdict: a service that
	// says it is stopping and one that refuses outright both keep serving, and
	// only the probe decides.
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("keeps serving after being answered %d", status), func(t *testing.T) {
			record, peer := fakeServicePeer(t, "token")
			peer.willNotStop(status)
			if handOverTo(runningServiceInfo{Record: record}, 300*time.Millisecond) {
				t.Fatal("a service that keeps answering must not be reported as gone")
			}
			if !peer.wasAskedToStop() {
				t.Fatal("it must be asked even so")
			}
		})
	}
	t.Run("is already gone", func(t *testing.T) {
		// A record left behind by a crash points at a port nothing listens on,
		// which must not make a relaunch wait or refuse.
		if !handOverTo(runningServiceInfo{Record: serviceRecord{Port: 1, Token: "gone"}},
			300*time.Millisecond) {
			t.Fatal("a service that is not there must be reported as gone")
		}
	})
}

func TestJoinServiceReusesALiveService(t *testing.T) {
	home := t.TempDir()
	record, _ := livePeer(t, home, "token")

	lock, existing, err := joinService(home, false, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if lock != nil {
		t.Fatal("reusing a service must not leave the startup lock held")
	}
	if existing.Port != record.Port || existing.Token != "token" {
		t.Fatalf("unexpected record: %+v", existing)
	}
}

func TestJoinServiceStartsWhenNothingIsRunning(t *testing.T) {
	home := t.TempDir()
	lock, existing, err := joinService(home, false, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if existing.Port != 0 {
		t.Fatalf("nothing was serving, yet a service was reported: %+v", existing)
	}
	if lock == nil {
		t.Fatal("a launch that starts a service must hold the lock while it starts")
	}
	// Holding it is what keeps a second launch from deciding to start one too.
	if other, err := lockService(home, 0); err == nil {
		other.Release()
		t.Fatal("the startup lock must exclude another launch")
	}
	lock.Release()
	// Releasing twice must stay harmless, and must really have given it back.
	lock.Release()
	other, err := lockService(home, 0)
	if err != nil {
		t.Fatalf("the lock must be free once it is given back: %v", err)
	}
	other.Release()
}

func TestJoinServiceWaitsForAConcurrentStartup(t *testing.T) {
	home := t.TempDir()
	record, _ := fakeServicePeer(t, "token")
	if _, alive := runningService(home); alive {
		t.Fatal("the test assumes nothing has been published yet")
	}
	// Another launch holds the lock and is still starting, so it has published
	// nothing: the record appears only once its API is listening.
	first, err := lockService(home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = writeServiceRecord(home, record)
		first.Release()
	}()

	// The port can only have been learnt by looking again after the lock was
	// given back, which is what the waiting is for.
	lock, existing, err := joinService(home, false, 5*time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if existing.Port != record.Port {
		t.Fatalf("the second launch should have waited and reused the first, got %+v", existing)
	}
	if lock != nil {
		t.Fatal("reusing a service must not leave the startup lock held")
	}
}

func TestJoinServiceTakesOverAServiceItOutranks(t *testing.T) {
	home := t.TempDir()
	_, peer := livePeer(t, home, "token")

	// A relaunch as administrator outranks a service that is not elevated.
	lock, existing, err := joinService(home, true, time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if existing.Port != 0 {
		t.Fatalf("a service that was taken over must not be reused: %+v", existing)
	}
	if lock == nil {
		t.Fatal("the launch that took over must hold the lock while it starts")
	}
	if !peer.wasAskedToStop() {
		t.Fatal("the old service must have been asked to shut down")
	}
	lock.Release()
}

func TestJoinServiceRefusesAServiceThatWillNotStop(t *testing.T) {
	home := t.TempDir()
	record, peer := livePeer(t, home, "token")
	peer.willNotStop(http.StatusOK)

	lock, existing, err := joinService(home, true, time.Second, 300*time.Millisecond)
	if err == nil {
		t.Fatal("a service that will not stop must not be replaced by a second one")
	}
	if lock != nil || existing.Port != 0 {
		t.Fatalf("a refused launch must report nothing and hold nothing: %v %+v", lock, existing)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(record.Port)) {
		t.Fatalf("the refusal should name the service to stop: %v", err)
	}
	// Giving up has to give the lock back, or every later launch would wait for
	// a launch that is no longer running.
	free, err := lockService(home, 0)
	if err != nil {
		t.Fatalf("the startup lock was left held: %v", err)
	}
	free.Release()
}

const (
	lockChildHomeEnv = "SMARTVPN_TEST_LOCK_HOME"
	lockChildWantEnv = "SMARTVPN_TEST_LOCK_WANT"
)

// TestServiceLockChild is not a test of its own: it is this binary run as a real
// second process, because a lock that only excludes its own process would be no
// lock at all.
func TestServiceLockChild(t *testing.T) {
	home := os.Getenv(lockChildHomeEnv)
	if home == "" {
		t.Skip("runs as a child of TestServiceLockHoldsAcrossProcesses")
	}
	lock, err := lockService(home, 500*time.Millisecond)
	if os.Getenv(lockChildWantEnv) == "acquired" {
		if err != nil {
			t.Fatalf("the freed lock should have been taken: %v", err)
		}
		lock.Release()
		return
	}
	if err == nil {
		lock.Release()
		t.Fatal("a second process must not take a lock that is held")
	}
}

func TestServiceLockHoldsAcrossProcesses(t *testing.T) {
	home := t.TempDir()
	held, err := lockService(home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// While it is held, another process must be refused rather than proceed.
	runLockChild(t, home, "refused")
	held.Release()
	// And once it is given back, that process must be able to take it.
	runLockChild(t, home, "acquired")
}

func runLockChild(t *testing.T, home, want string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestServiceLockChild$", "-test.v")
	command.Env = append(os.Environ(), lockChildHomeEnv+"="+home, lockChildWantEnv+"="+want)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the child process failed (%v):\n%s", err, out)
	}
}
