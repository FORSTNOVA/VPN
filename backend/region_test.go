package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLooksLikeCountryCode(t *testing.T) {
	valid := []string{"JP", "US", "CN", "GB"}
	for _, code := range valid {
		if !looksLikeCountryCode(code) {
			t.Errorf("%q should be a country code", code)
		}
	}
	invalid := []string{"", "J", "JPN", "jp", "J1", "-P", "J P"}
	for _, code := range invalid {
		if looksLikeCountryCode(code) {
			t.Errorf("%q should not be a country code", code)
		}
	}
}

func TestParseGeoAnswer(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantIP      string
		wantCountry string
		wantErr     bool
	}{
		{name: "geojs shape", body: `{"ip":"1.2.3.4","country_code":"JP"}`, wantIP: "1.2.3.4", wantCountry: "JP"},
		{name: "ipwho.is shape", body: `{"ip":"1.2.3.4","success":true,"country_code":"us"}`, wantIP: "1.2.3.4", wantCountry: "US"},
		{name: "ipv6 answer", body: `{"ip":"2001:db8::1","country_code":"DE"}`, wantIP: "2001:db8::1", wantCountry: "DE"},
		{name: "padded values", body: `{"ip":" 1.2.3.4 ","country_code":" jp "}`, wantIP: "1.2.3.4", wantCountry: "JP"},
		{name: "explicit failure", body: `{"ip":"","country_code":"","success":false}`, wantErr: true},
		{name: "missing address", body: `{"country_code":"JP"}`, wantErr: true},
		{name: "unparsable address", body: `{"ip":"not-an-ip","country_code":"JP"}`, wantErr: true},
		{name: "missing country", body: `{"ip":"1.2.3.4"}`, wantErr: true},
		{name: "country is not a code", body: `{"ip":"1.2.3.4","country_code":"Japan"}`, wantErr: true},
		{name: "malformed json", body: `{"ip":`, wantErr: true},
		{name: "empty body", body: ``, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip, country, err := parseGeoAnswer([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got (%q, %q)", ip, country)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGeoAnswer: %v", err)
			}
			if ip != tc.wantIP || country != tc.wantCountry {
				t.Fatalf("got (%q, %q), want (%q, %q)", ip, country, tc.wantIP, tc.wantCountry)
			}
		})
	}
}

func TestCombineRegionAnswers(t *testing.T) {
	cases := []struct {
		name        string
		results     []regionProbeResult
		wantStatus  string
		wantCountry string
		wantSources int
	}{
		{name: "no source answered", results: nil, wantStatus: regionUnreachable},
		{
			name: "one source is not enough to lock",
			results: []regionProbeResult{
				{ExitIP: "1.2.3.4", Country: "JP", Sources: []string{"geojs.io"}},
			},
			wantStatus: regionSingle, wantCountry: "JP", wantSources: 1,
		},
		{
			name: "two agreeing sources verify the region",
			results: []regionProbeResult{
				{ExitIP: "1.2.3.4", Country: "JP", Sources: []string{"geojs.io"}},
				{ExitIP: "1.2.3.4", Country: "JP", Sources: []string{"ipwho.is"}},
			},
			wantStatus: regionVerified, wantCountry: "JP", wantSources: 2,
		},
		{
			name: "a country mismatch is a conflict",
			results: []regionProbeResult{
				{ExitIP: "1.2.3.4", Country: "JP", Sources: []string{"geojs.io"}},
				{ExitIP: "5.6.7.8", Country: "US", Sources: []string{"ipwho.is"}},
			},
			wantStatus: regionConflict, wantCountry: "JP", wantSources: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := combineRegionAnswers(tc.results)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Country != tc.wantCountry {
				t.Errorf("country = %q, want %q", got.Country, tc.wantCountry)
			}
			if len(got.Sources) != tc.wantSources {
				t.Errorf("sources = %v, want %d entries", got.Sources, tc.wantSources)
			}
			if got.Detail == "" {
				t.Error("every outcome must explain itself to the user")
			}
		})
	}
}

func newTestApp(t *testing.T) *app {
	t.Helper()
	return &app{
		home:    t.TempDir(),
		health:  map[string]nodeHealth{},
		regions: map[string]regionRecord{},
	}
}

func TestLockRegionRequiresAVerifiedNode(t *testing.T) {
	now := time.Now()
	a := newTestApp(t)
	a.regions = map[string]regionRecord{
		"verified":  {Name: "verified", Country: "JP", Status: regionVerified, VerifiedAt: now},
		"single":    {Name: "single", Country: "US", Status: regionSingle, VerifiedAt: now},
		"stale":     {Name: "stale", Country: "DE", Status: regionVerified, VerifiedAt: now.Add(-2 * regionTTL)},
		"conflict":  {Name: "conflict", Country: "GB", Status: regionConflict, VerifiedAt: now},
		"unreached": {Name: "unreached", Country: "FR", Status: regionUnreachable, VerifiedAt: now},
	}

	for _, country := range []string{"US", "DE", "GB", "FR", "ZZ"} {
		recorder := httptest.NewRecorder()
		a.lockRegion(recorder, httptest.NewRequest(http.MethodPut, "/api/region/lock",
			strings.NewReader(`{"country":"`+country+`"}`)))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("locking %q returned %d, want 400: a region needs a verified node", country, recorder.Code)
		}
	}
	if a.lockedRegion != "" {
		t.Fatalf("a rejected lock must not change the locked region, got %q", a.lockedRegion)
	}
}

func TestLockRegionAcceptsVerifiedCountry(t *testing.T) {
	now := time.Now()
	a := newTestApp(t)
	a.regions = map[string]regionRecord{
		"verified": {Name: "verified", Country: "JP", Status: regionVerified, VerifiedAt: now},
		"second":   {Name: "second", Country: "JP", Status: regionVerified, VerifiedAt: now},
	}
	recorder := httptest.NewRecorder()
	a.lockRegion(recorder, httptest.NewRequest(http.MethodPut, "/api/region/lock",
		strings.NewReader(`{"country":"jp"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if a.lockedRegion != "JP" {
		t.Fatalf("lockedRegion = %q, want JP", a.lockedRegion)
	}
	if a.settings.LockedRegion != "JP" {
		t.Fatalf("the lock must survive a restart, got %q", a.settings.LockedRegion)
	}
	// The pinned node must belong to the region the lock claims, otherwise the
	// group could keep carrying an off-region node.
	if a.lockedNode != "verified" && a.lockedNode != "second" {
		t.Fatalf("lockedNode = %q, want one of the JP candidates", a.lockedNode)
	}
	if a.settings.LockedNode != a.lockedNode {
		t.Fatalf("the pinned node must be persisted: %q / %q", a.settings.LockedNode, a.lockedNode)
	}
	if !strings.Contains(recorder.Body.String(), `"pool":2`) {
		t.Errorf("response should report the pool size: %s", recorder.Body.String())
	}

	// Clearing the lock must not require a verified region.
	recorder = httptest.NewRecorder()
	a.lockRegion(recorder, httptest.NewRequest(http.MethodPut, "/api/region/lock",
		strings.NewReader(`{"country":""}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 when clearing the lock", recorder.Code)
	}
	if a.lockedRegion != "" {
		t.Fatalf("lockedRegion = %q, want an empty lock", a.lockedRegion)
	}
}

func TestLockRegionRejectsMalformedRequests(t *testing.T) {
	a := newTestApp(t)
	for _, body := range []string{`{`, `{"country":"JPN"}`, `{"country":"J1"}`, `{"unexpected":1}`} {
		recorder := httptest.NewRecorder()
		a.lockRegion(recorder, httptest.NewRequest(http.MethodPut, "/api/region/lock", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %q returned %d, want 400", body, recorder.Code)
		}
	}
}

func TestLockRegionToFollowsAVerifiedNode(t *testing.T) {
	now := time.Now()
	a := newTestApp(t)
	a.regions = map[string]regionRecord{
		"jp":    {Name: "jp", Country: "JP", Status: regionVerified, VerifiedAt: now},
		"stale": {Name: "stale", Country: "US", Status: regionVerified, VerifiedAt: now.Add(-2 * regionTTL)},
		"weak":  {Name: "weak", Country: "DE", Status: regionSingle, VerifiedAt: now},
	}

	a.lockRegionToLocked("jp")
	if a.lockedRegion != "JP" {
		t.Fatalf("lockedRegion = %q, want JP", a.lockedRegion)
	}

	// A stale or single-source node must not move the lock.
	a.lockRegionToLocked("stale")
	a.lockRegionToLocked("weak")
	a.lockRegionToLocked("unknown")
	a.lockRegionToLocked("")
	if a.lockedRegion != "JP" {
		t.Fatalf("lockedRegion = %q, want the lock to stay on JP", a.lockedRegion)
	}
}

func TestStartRegionVerificationRequiresSubscription(t *testing.T) {
	a := newTestApp(t)
	recorder := httptest.NewRecorder()
	a.startRegionVerification(recorder, httptest.NewRequest(http.MethodPost, "/api/regions/verify",
		strings.NewReader(`{}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 without a subscription", recorder.Code)
	}
}

func TestStartRegionVerificationRejectsConcurrentJobs(t *testing.T) {
	a := newTestApp(t)
	a.settings = settings{SubscriptionURL: "https://example.com/sub", MihomoPath: filepath.Join(t.TempDir(), "missing.exe")}
	a.regionJob = &regionJob{Running: true}

	recorder := httptest.NewRecorder()
	a.startRegionVerification(recorder, httptest.NewRequest(http.MethodPost, "/api/regions/verify",
		strings.NewReader(`{}`)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 while a check is running", recorder.Code)
	}
}

func TestStartRegionVerificationAcceptsAnEmptyBody(t *testing.T) {
	a := newTestApp(t)
	a.settings = settings{SubscriptionURL: "https://example.com/sub", MihomoPath: filepath.Join(t.TempDir(), "missing.exe")}

	recorder := httptest.NewRecorder()
	a.startRegionVerification(recorder, httptest.NewRequest(http.MethodPost, "/api/regions/verify", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	// The background job cannot start Mihomo here, so it reports the failure
	// instead of leaving the job flagged as running.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		running := a.regionJobRunningLocked()
		lastError := ""
		if a.regionJob != nil {
			lastError = a.regionJob.LastError
		}
		a.mu.Unlock()
		if !running {
			if lastError == "" {
				t.Fatal("a job that could not start must record why")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the region job never finished")
}

func TestRegionJobSnapshotIsACopy(t *testing.T) {
	a := newTestApp(t)
	a.regionJob = &regionJob{Running: true, Total: 10, Done: 4}
	snapshot := a.regionJobSnapshotLocked()
	snapshot.Done = 99
	if a.regionJob.Done != 4 {
		t.Fatalf("the snapshot must not alias the live job, got %d", a.regionJob.Done)
	}
	if empty := (&app{}).regionJobSnapshotLocked(); empty.Running {
		t.Fatal("a missing job must read as not running")
	}
}

// A region locked while a connection is running has to bind that connection,
// not only the next one. The window would otherwise say the region is locked
// while the group still carries another region's node, which is the one thing
// the lock exists to prevent.
func TestLockingWhileConnectedPinsTheRunningGroup(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	runningKernel(t, a)
	now := time.Now()
	for _, record := range []regionRecord{
		{Name: "jp-a", Country: "JP"},
		{Name: "jp-b", Country: "JP"},
		{Name: "sg-a", Country: "SG"},
	} {
		record.Status = regionVerified
		record.VerifiedAt = now
		a.regions[record.Name] = record
	}

	recorder := httptest.NewRecorder()
	a.lockRegion(recorder, httptest.NewRequest(http.MethodPost, "/api/region/lock",
		strings.NewReader(`{"country":"JP"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if a.lockedRegion != "JP" || a.lockedNode == "" {
		t.Fatalf("locked region = %q, node = %q", a.lockedRegion, a.lockedNode)
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"`+a.lockedNode+`"`) {
		t.Fatalf("the running group must be moved onto the locked region, calls: %v", stub.calls)
	}
	if stub.saw(http.MethodPut, "/proxies/SmartVPN", "REJECT") {
		t.Fatalf("a region with candidates must not be blocked, calls: %v", stub.calls)
	}
}

// Locking a region whose nodes cannot carry traffic fails closed, rather than
// leaving the connection on the region the user just moved away from.
func TestLockingWhileConnectedWithOnlyCoolingNodesBlocks(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	runningKernel(t, a)
	now := time.Now()
	a.regions["jp-a"] = regionRecord{Name: "jp-a", Country: "JP", Status: regionVerified, VerifiedAt: now}
	// Verified, but parked after failing: not a candidate for anything.
	a.health["jp-a"] = nodeHealth{
		Name: "jp-a", State: healthCooldown, CooldownUntil: now.Add(time.Hour),
	}

	recorder := httptest.NewRecorder()
	a.lockRegion(recorder, httptest.NewRequest(http.MethodPost, "/api/region/lock",
		strings.NewReader(`{"country":"JP"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if a.lockedNode != "" {
		t.Fatalf("a node in cooldown is not a candidate, got %q", a.lockedNode)
	}
	if !a.blocked {
		t.Fatalf("the region cannot carry traffic, so the connection must fail closed: %v", stub.calls)
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", "REJECT") {
		t.Fatalf("the group should have been pointed at REJECT, calls: %v", stub.calls)
	}
}

// Nothing is switched when there is no connection to switch.
func TestLockingWhileDisconnectedTouchesNoGroup(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Now()
	a.regions["jp-a"] = regionRecord{Name: "jp-a", Country: "JP", Status: regionVerified, VerifiedAt: now}

	recorder := httptest.NewRecorder()
	a.lockRegion(recorder, httptest.NewRequest(http.MethodPost, "/api/region/lock",
		strings.NewReader(`{"country":"JP"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if a.lockedRegion != "JP" {
		t.Fatalf("locked region = %q", a.lockedRegion)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("a lock without a connection must not talk to the kernel, calls: %v", stub.calls)
	}
}
