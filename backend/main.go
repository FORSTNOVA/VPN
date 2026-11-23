package main

import (
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type trafficPolicy struct {
	GameBypass       []string `json:"gameBypass,omitempty"`
	DomesticWeb      bool     `json:"domesticWeb"`
	WebRTCProtection bool     `json:"webRTCProtection"`
	DNSProtection    bool     `json:"dnsProtection"`
}

func (p trafficPolicy) active() bool {
	return p.DomesticWeb || p.WebRTCProtection || p.DNSProtection
}

type settings struct {
	ImportPolicy bool `json:"importPolicy,omitempty"`
	trafficPolicy
	Chains          map[string][]string `json:"chains,omitempty"`
	Chain           proxyChain          `json:"chain,omitempty"`
	SubscriptionURL string              `json:"subscriptionUrl"`
	MihomoPath      string              `json:"mihomoPath"`
	SelectedNode    string              `json:"selectedNode,omitempty"`
	SelectionMode   string              `json:"selectionMode,omitempty"`
	LockedRegion    string              `json:"lockedRegion,omitempty"`
	LockedNode      string              `json:"lockedNode,omitempty"`
	ConnectionMode  string              `json:"connectionMode,omitempty"`
	StrictPrivacy   bool                `json:"strictPrivacy,omitempty"`
	WintunPath      string              `json:"wintunPath,omitempty"`
	AutoMergeHours  int                 `json:"autoMergeHours,omitempty"`
	SocksPort       int                 `json:"socksPort,omitempty"`
}

// chainGraphSettings deliberately retains the original resource graph and aliases.
// Ordinary control settings exist only in the persistent service authority.
type chainGraphSettings struct {
	Chain  proxyChain
	Chains map[string][]string
}

// settingsDocument temporarily joins authority projections and the Go chain graph.
type settingsDocument struct {
	settings
	PathChecks pathCheckSettings `json:"pathChecks,omitempty"`
}

func (a *app) settingsDocumentLocked() settingsDocument {
	return settingsDocument{settings: a.settingsLocked(), PathChecks: a.pathConfigLocked()}
}
func (a *app) decodeSettingsDocumentLocked(body []byte) error {
	doc := a.settingsDocumentLocked()
	err := json.Unmarshal(body, &doc)
	// encoding/json may retain successfully decoded fields after a type error.
	// Commit that exact partial result, including unchanged fields from the seed.
	a.restoreDecodedControlLocked(doc.settings)
	a.replaceChainsLocked(chainGraphSettings{Chain: doc.Chain, Chains: doc.Chains})
	a.replacePathConfigLocked(doc.PathChecks)
	return err
}

const (
	autoGroup     = "SmartVPNAuto"
	fallbackGroup = "SmartVPNFallback"
	blockedChoice = "REJECT"
	// appName is what the identity route answers with, so one copy of this app
	// can recognise another one serving the machine's proxy setting.
	appName = "SmartVPN"
)

type app struct {
	policyPreview   policyPreviewCache
	mu              sync.Mutex
	home            string
	settings        chainGraphSettings
	kernel          kernelRunner
	rememberingNode bool
	lastSavedNode   string
	dnsRelay        *adaptiveDNS
	started         time.Time
	proxyOn         bool
	ctrlSecret      string
	mixedPort       int
	ctrlPort        int
	shutdown        chan struct{}
	cachedNodes     []proxyNode
	nodeGraph       graphCommands
	nodeCache       nodeCacheStore
	cacheBorrowed   bool
	store           *store
	autoMergeCancel chan struct{}
	patrolCancel    chan struct{}
	// Set when a sweep condemned every node of the locked region while a real
	// request still got through, which is a measurement problem rather than a
	// dead region. It says so on the connection page until a sweep measures
	// something again.
	// Replaces the confirmation probe in tests: the real one asks an HTTPS
	// address through the tunnel, which a test server cannot answer without a
	// certificate for it.
	confirmProbe func() bool
	// Strips subscription URLs from everything this service logs.
	redactor *logRedactor
	// Replaces the Windows proxy read and the published ownership record in
	// tests, which would otherwise consult this machine's own registry.
	proxyStateHook func() (bool, string, error)
	proxyOwnerHook func() (proxyOwner, bool)
	// The port this service itself listens on, which is what the ownership
	// record publishes: the proxy setting names the kernel's port, and only this
	// one can answer whether a copy is still there.
	apiPort  int
	elevated bool
	// The previous counter reading, which turns two totals into a rate.
	service              *serviceAuthority
	trafficHistoryCancel chan struct{}
	// The nodes pasted by hand, which live in their own provider file.
	manualNodes        []manualNode
	chainMirrorHash    [32]byte
	manualRecordHashes [][32]byte // last published version of each compatibility row
	// Where the executable sits, which is where a packaged copy keeps the kernel,
	// and whether this copy keeps its data beside it.
	bundleDir string
	portable  bool
	// The path monitor: its timer, the last round's answers, and a guard so two
	// rounds never overlap.
	pathCancel chan struct{}
	// Download resource replaced by deterministic fixtures in tests.
	chinaFetch func() ([]byte, string, error)
}

type bootstrap struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	// Set instead of a port when this launch decided not to serve at all, so
	// the window can say why rather than waiting for a line that never comes.
	Error string `json:"error,omitempty"`
}

type proxyNode struct {
	SourceLabel string `json:"sourceLabel,omitempty"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Network     string `json:"network,omitempty"`
	TLS         bool   `json:"tls"`
	WsHost      bool   `json:"wsHostConfigured"`
	WsPath      bool   `json:"wsPathConfigured"`
	Alive       bool   `json:"alive"`
	Selected    bool   `json:"selected"`
	// True for a node the user pasted rather than one the subscription lists.
	Manual bool `json:"manual,omitempty"`
}

type cachedNodeList struct {
	SubscriptionHash string      `json:"subscriptionHash"`
	Nodes            []proxyNode `json:"nodes"`
}

type subscriptionReport struct {
	RequestQueryKeys []string      `json:"requestQueryKeys"`
	UserAgent        string        `json:"userAgent"`
	FlagAdded        bool          `json:"flagAdded"`
	AppliedFormat    string        `json:"appliedFormat,omitempty"`
	Fetch            fetchReport   `json:"fetch"`
	Cache            payloadReport `json:"cache"`
}

type fetchReport struct {
	StatusCode  int           `json:"statusCode,omitempty"`
	ContentType string        `json:"contentType,omitempty"`
	Bytes       int           `json:"bytes"`
	Error       string        `json:"error,omitempty"`
	Payload     payloadReport `json:"payload"`
}

type payloadReport struct {
	Present     bool           `json:"present"`
	Format      string         `json:"format"`
	Bytes       int            `json:"bytes"`
	Selectable  int            `json:"selectableNodes"`
	UriCounts   map[string]int `json:"uriCounts"`
	ValidVmess  int            `json:"validVmess"`
	InfoEntries int            `json:"infoEntries"`
	Networks    map[string]int `json:"networks"`
	TlsModes    map[string]int `json:"tlsModes"`
	WsHostSet   int            `json:"wsHostSet"`
	WsPathSet   int            `json:"wsPathSet"`
}

type vmessDetails struct {
	Name string `json:"ps"`
	Net  string `json:"net"`
	TLS  string `json:"tls"`
	Host string `json:"host"`
	Path string `json:"path"`
}

// Match Clash Verge's default profile User-Agent so the same subscription
// endpoint can return its Clash-compatible response.
const subscriptionUserAgent = "clash-verge/v2.5.6"

// openServiceLog points the standard logger at this profile's log, through a
// redactor. Everything this service logs goes through one, so the promise that
// a subscription URL never reaches its own log holds at the sink instead of
// depending on every call site being written carefully. The file is left open
// for the process's lifetime; the profile directory is the caller's job.
func openServiceLog(home string) *logRedactor {
	redactor := newLogRedactor(os.Stderr)
	if logFile, err := os.OpenFile(filepath.Join(home, "service.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		redactor.out = logFile
	}
	log.SetOutput(redactor)
	return redactor
}

// newService builds the service for a profile directory: the settings, the
// database and everything cached beside them. It starts nothing and binds
// nothing, because the profile is not this launch's to write to until it has
// been decided that this launch is the one that serves.
func newService(home, bundleDir string, portable, elevated bool) *app {
	a := &app{home: home, bundleDir: bundleDir, portable: portable, elevated: elevated}
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "new"})
	if r := runLifecycleWithEffects(e.q, e); r.Err != nil {
		panic(r.Err)
	}
	return a
}

// prepare finishes what newService started. It runs once this launch has been
// decided to be the one that serves, because the first thing it does can take
// the machine's proxy setting away from a copy that is still using it.
func (a *app) prepare(redactor *logRedactor) {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "prepare"})
	e.redactor = redactor
	if r := runLifecycleWithEffects(e.q, e); r.Err != nil {
		panic(r.Err)
	}
}

// newToken mints the two secrets this run uses: the one the window presents to
// the local API, and the one the kernel's control interface answers to. They
// are separate so that a leak of one is not a leak of the other.
func (a *app) newToken() string {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		log.Fatal(err)
	}
	ctrlBytes := make([]byte, 32)
	if _, err := rand.Read(ctrlBytes); err != nil {
		log.Fatal(err)
	}
	a.ctrlSecret = hex.EncodeToString(ctrlBytes)
	return hex.EncodeToString(tokenBytes)
}

// serveLocalAPI binds the loopback API and starts answering on it. The service
// is bound to 127.0.0.1 and nowhere else, so nothing off this machine can reach
// the port at all and the token is the only thing a local caller needs.
func (a *app) serveLocalAPI(token string) (int, *http.Server, error) {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "serve"})
	e.token = token
	r := runLifecycleWithEffects(e.q, e)
	return r.Port, r.Server, r.Err
}

// routes is the whole local API. Every route but the identity one asks for the
// token; a platform adds the ones only it can serve.
func (a *app) routes(token string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, route := range a.apiRoutes() {
		pattern := route.method + " " + route.path
		mux.HandleFunc(pattern, httpDispatchWrapper(pattern, token, route.id, route.id == 1, route.handle))
	}
	return mux
}

// shutdownAndCleanup stops the kernel and puts back whatever this run changed.
func (a *app) shutdownAndCleanup() {
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.closeServiceStateLocked(); err != nil {
			log.Printf("service state close failed: %v", err)
		}
	}()
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "cleanup"})
	if r := runLifecycleWithEffects(e.q, e); r.Err != nil {
		log.Printf("service cleanup interrupted: %v", r.Err)
	}
}

func (a *app) authorize(token string, next http.HandlerFunc) http.HandlerFunc {
	return httpDispatchWrapper("", token, 0, false, next)
}
func (a *app) state(w http.ResponseWriter, _ *http.Request) {
	q := appServiceRequest{Op: "state"}
	e := newAppControlEffects(a, q)
	e.w = w
	runAppControlWithEffects(q, e)
}

func (a *app) reapplyProxy(w http.ResponseWriter, _ *http.Request) {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "reapply"})
	e.w = w
	runLifecycleHTTP(e)
}

// settingsPatch is what this route accepts. Every field is a pointer, because
// absent and empty are different things: a field the caller left out keeps its
// value, while a field sent as an empty string is a caller asking for it to be
// cleared.
//
// The route used to replace the whole settings structure, which made an omitted
// field indistinguishable from one meant to be cleared. A request that only
// switched the connection mode therefore dropped the subscription with it — and
// with the subscription went the cached profile, the selected node, the region
// lock and the health data, all of it irreversibly. Only the fields the settings
// page shows are accepted here; the selection, the region lock and the path
// monitor are owned by their own routes and cannot be changed through this one.
type settingsPatch struct {
	ImportPolicy     *bool     `json:"importPolicy"`
	SocksPort        *int      `json:"socksPort"`
	GameBypass       *[]string `json:"gameBypass"`
	DomesticWeb      *bool     `json:"domesticWeb"`
	WebRTCProtection *bool     `json:"webRTCProtection"`
	DNSProtection    *bool     `json:"dnsProtection"`
	SubscriptionURL  *string   `json:"subscriptionUrl"`
	MihomoPath       *string   `json:"mihomoPath"`
	WintunPath       *string   `json:"wintunPath"`
	ConnectionMode   *string   `json:"connectionMode"`
	StrictPrivacy    *bool     `json:"strictPrivacy"`
}

var gameExeName = regexp.MustCompile(`(?i)^[\p{L}\p{N} _.-]+\.exe$`)
var gamePackageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

func windowsGameRuleGo(entry string) string {
	if gameExeName.MatchString(entry) {
		return "PROCESS-NAME," + entry + ",DIRECT"
	}
	if !filepath.IsAbs(entry) || !strings.EqualFold(filepath.Ext(entry), ".exe") ||
		strings.ContainsAny(entry, "\r\n\x00*?") {
		return ""
	}
	path := filepath.Clean(entry)
	if strings.Contains(path, ",") {
		// A comma would split a plain Mihomo rule. Encode this literal in
		// an anchored regex so a real filename containing commas still works.
		return "PROCESS-PATH-REGEX,^" + strings.ReplaceAll(regexp.QuoteMeta(path), ",", `\x2c`) + "$,DIRECT"
	}
	return "PROCESS-PATH," + path + ",DIRECT"
}

func normalizeGameBypassGo(entries []string, android bool) ([]string, error) {
	result := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		name := strings.TrimSpace(entry)
		if name == "" {
			continue
		}
		key := name
		if android {
			if !gamePackageName.MatchString(name) {
				return nil, errors.New("每行请输入一个完整的 Android 应用包名")
			}
		} else {
			if windowsGameRuleGo(name) == "" {
				return nil, errors.New("请选择游戏 .exe，或输入完整路径或进程名")
			}
			if filepath.IsAbs(name) {
				name = filepath.Clean(name)
			}
			key = strings.ToLower(name)
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, name)
		}
	}
	return result, nil
}

func (a *app) updateSettings(w http.ResponseWriter, r *http.Request) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	w = tracked
	defer tracked.recoverFailure()
	var patch settingsPatch
	if err := decodeJSON(r.Body, &patch); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	modeSupported := true
	if patch.ConnectionMode != nil {
		modeSupported = connectionModeSupported(strings.TrimSpace(*patch.ConnectionMode))
	}
	request := prepareSettingsServiceRequest(patch, runtime.GOOS == "windows", runtime.GOOS == "android", a.apiPort, modeSupported)
	state := settingsStateFromApp(a)
	fx := &httpSettingsEffects{appSettingsEffects: appSettingsEffects{a}}
	result := runSelectedSettingsService(&state, request, fx)
	if result.Status != 200 {
		http.Error(w, result.Message, result.Status)
		return
	}
	if err := fx.commit(state); err != nil {
		http.Error(w, "could not save settings", 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *app) connect(w http.ResponseWriter, req *http.Request) {
	writeConnectionServiceResult(w, a.connectionService(connectionServiceRequest{Op: "connect"}, req.URL.Query().Get("resume") == "last"))
}

func (a *app) disconnect(w http.ResponseWriter, _ *http.Request) {
	writeConnectionServiceResult(w, a.runConnectionService(connectionServiceRequest{Op: "disconnect"}))
}

// dropConnectionLocked ends a connection: the kernel, the patrol that watches
// it and the path monitor that measures it, and whatever the platform was asked
// to change on the machine's behalf.
//
// It is not only the disconnect route that needs this. A connection can end
// because the ground moved under it — the kernel died, or the system took the
// tunnel away — and the same unwinding has to happen then, without a caller
// waiting on a reply.
func (a *app) dropConnectionLocked() error {
	return a.runConnectionService(connectionServiceRequest{Op: "drop"}).Err
}

func (a *app) shutdownService(w http.ResponseWriter, r *http.Request) {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "shutdown"})
	e.w = w
	e.request = r
	runLifecycleHTTP(e)
}

func (a *app) latency(w http.ResponseWriter, _ *http.Request) {
	runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "latency"}, w)
}

func chooseReachableNodeGo(nodes []proxyNode, delays map[string]int, preferred string) (string, int) {
	bestName, bestDelay := "", 0
	for _, node := range nodes {
		delay := delays[node.Name]
		if delay <= 0 {
			continue
		}
		if node.Name == preferred {
			return node.Name, delay
		}
		if bestDelay == 0 || delay < bestDelay {
			bestName, bestDelay = node.Name, delay
		}
	}
	return bestName, bestDelay
}

func (a *app) groupDelays(timeout time.Duration) (map[string]int, error) {
	return a.groupDelaysForURL(patrolTargetPrimary, timeout)
}

func (a *app) groupDelaysForURL(target string, timeout time.Duration) (map[string]int, error) {
	r := runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "delay", Target: target, Timeout: timeout}, nil)
	return r.Delays, r.Err
}

func (a *app) nodes(w http.ResponseWriter, _ *http.Request) {
	runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "nodes"}, w)
}

// nodeLatency measures every node in the group once. The node list needs
// comparable numbers for the nodes the health sweep never touches, and Mihomo
// can only be asked for a whole group at a time.
func (a *app) nodeLatency(w http.ResponseWriter, _ *http.Request) {
	runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "node-latency"}, w)
}

func (a *app) fetchNodes(w http.ResponseWriter, _ *http.Request) {
	a.runSubscriptionHTTP("fetch", w)
}

func (a *app) subscriptionDiagnostics(w http.ResponseWriter, _ *http.Request) {
	a.runSubscriptionHTTP("diagnostics", w)
}

func (a *app) refreshNodes(w http.ResponseWriter, _ *http.Request) {
	a.runSubscriptionHTTP("refresh", w)
}

func (a *app) selectNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r.Body, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		http.Error(w, "invalid node selection", http.StatusBadRequest)
		return
	}
	writeConnectionServiceResult(w, a.runConnectionService(connectionServiceRequest{Op: "select", Name: body.Name}))
}

func (a *app) listNodes() ([]proxyNode, error) {
	r := runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "list"}, nil)
	return r.Nodes, r.Err
}

func (a *app) waitForNodes(timeout time.Duration) error {
	return runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "wait", Timeout: timeout}, nil).Err
}

func (a *app) mihomoRequest(method, route string, body any) (map[string]any, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(a.ctrlPort)+route, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.ctrlSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Mihomo returned HTTP %d", resp.StatusCode)
	}
	result := map[string]any{}
	if (method == http.MethodPut && strings.HasPrefix(route, "/proxies/")) || resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
		return result, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *app) waitForMihomo(timeout time.Duration) error {
	return a.runKernelAdmission(kernelAdmissionRequest{Op: "ready", Timeout: timeout}).Err
}

// systemProxyEnabled reports whether the machine's proxy is on and pointing at
// this service, which is what the connection page shows.
func (a *app) systemProxyEnabled() (bool, error) {
	enabled, server, err := a.proxyState()
	if err != nil {
		return false, err
	}
	return enabled && server == net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort)), nil
}

// proxyState reads the setting as the system holds it: whether it is on, and
// the address it points at, whoever set it.
func (a *app) proxyState() (bool, string, error) {
	if a.proxyStateHook != nil {
		return a.proxyStateHook()
	}
	return readWindowsProxyState()
}

// identify is the one route that asks for no token, and it is deliberately
// small. A copy of this app has to be able to tell whether the machine's proxy
// is being served by another copy, and the address of that server is all it has:
// the other copy's profile directory, and therefore its token, are its own. The
// answer names the app and whether its kernel is running, and says nothing about
// the user. The service is bound to the loopback interface, so only processes
// running as this user can ask.
func (a *app) identify(w http.ResponseWriter, _ *http.Request) {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "identify"})
	e.w = w
	runLifecycleHTTP(e)
}

func (a *app) loadSettings() {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "load-settings"})
	if r := runLifecycleWithEffects(e.q, e); r.Err != nil {
		panic(r.Err)
	}
}

func (a *app) selectedChoice() string {
	if a.chainEnabledLocked() {
		return chainExit
	}
	c := a.controlMetaLocked()
	// A locked region owns the selection, so Mihomo's own automatic groups can
	// never pick a node outside the region the user pinned.
	if c.RuntimeLockedRegion != "" && c.RuntimeLockedNode != "" {
		return c.RuntimeLockedNode
	}
	switch c.SelectionMode {
	case "fallback":
		return fallbackGroup
	case "manual":
		return c.SelectedNode
	default:
		return autoGroup
	}
}

func (a *app) cachedNodesPath() string {
	return filepath.Join(a.home, "nodes-cache.json")
}

func (a *app) configPath() string {
	return filepath.Join(a.home, fmt.Sprintf("config-%d.yaml", os.Getpid()))
}

func (a *app) removeConfigLocked() {
	_ = os.Remove(a.configPath())
}

// A forced termination skips the stop path, so drop configs left by earlier runs.
func (a *app) sweepStaleConfigs() {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "sweep"})
	if r := runLifecycleWithEffects(e.q, e); r.Err != nil {
		panic(r.Err)
	}
}

func subscriptionHash(subscriptionURL string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(subscriptionURL)))
}

func (a *app) loadCachedNodes() {
	e := newAppLifecycleEffects(a, lifecycleServiceRequest{Op: "cache"})
	if r := runLifecycleWithEffects(e.q, e); r.Err != nil {
		panic(r.Err)
	}
}

func (a *app) saveCachedNodesLocked(nodes []proxyNode) error {
	cache := cachedNodeList{
		SubscriptionHash: subscriptionHash(a.controlMetaLocked().SubscriptionURL),
		Nodes:            append([]proxyNode(nil), nodes...),
	}
	body, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	path := a.cachedNodesPath()
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func (a *app) clearCachedNodesLocked() {
	a.replaceCachedNodesLocked(nil, false)
	_ = os.Remove(a.cachedNodesPath())
}

func (a *app) saveSettings() error {
	if err := a.writeSettingsDocument(a.settingsDocumentLocked()); err != nil {
		return err
	}
	a.refreshSettingsRedactor()
	return nil
}
func (a *app) writeSettingsDocument(stored settingsDocument) error {
	tmp, err := a.prepareSettingsDocument(stored)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Rename(tmp, filepath.Join(a.home, "settings.json"))
}
func (a *app) prepareSettingsDocument(stored settingsDocument) (string, error) {
	sealed, err := sealSecret(stored.SubscriptionURL)
	if err != nil {
		return "", err
	}
	stored.SubscriptionURL = sealed
	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return "", err
	}
	return stagePrivateFile(a.home, b)
}
func stagePrivateFile(dir string, b []byte) (string, error) {
	file, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return "", err
	}

	_, writeErr := file.Write(b)
	closeErr := file.Close()
	if writeErr != nil {
		os.Remove(file.Name())
		return "", writeErr
	}
	if closeErr != nil {
		os.Remove(file.Name())
		return "", closeErr
	}
	return file.Name(), nil
}
func (a *app) refreshSettingsRedactor() {
	if a.redactor != nil {
		a.redactor.Set(a.subscriptionSecrets())
	}
}

func clashRSFilename(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "clash-rs.exe", "clash-rs-x86_64-pc-windows-msvc.exe":
		return true
	}
	return false
}

type configOptions struct {
	ClashRS          bool
	dnsRelayAddr     string
	SourceRuleOrder  bool
	ChainProxies     []byte
	MixedPort        int
	ControllerPort   int
	ProviderPath     string
	ControllerSecret string
	Rules            []string
	TUN              bool
	trafficPolicy
	// Which providers the groups may draw nodes from. A machine that has only
	// pasted nodes must not be handed a subscription file that does not exist.
	SubscriptionProvider bool
	ManualProvider       bool
	// Hostname suffixes of the subscription's own node servers, which must stay
	// out of the fake-ip pool.
	ProxyServerDomains []string
	EntryDNS           map[string][]string
	// Whether the cached list of the country's own addresses is in place, which
	// is what keeps domestic traffic from being carried by the proxy.
	ChinaDirect bool
}

// A node that cannot relay UDP makes QUIC hang, and clients that try HTTP/3 then
// report a failed request instead of falling back to TCP. Refusing UDP:443 makes
// them retry over TLS immediately.
//
// It is emitted after the rules that decide a connection themselves and before
// the ones that hand it to the proxy. Refusing QUIC ahead of a DIRECT rule would
// take HTTP/3 away from domestic sites that serve it, and leaving it behind a
// proxy rule would let that rule claim the datagram first, which is the hang the
// rule exists to prevent.
const quicRejectRule = "AND,((NETWORK,udp),(DST-PORT,443)),REJECT"

// chinaDirectRule sends the country's own addresses direct. It resolves the name
// it is given rather than trusting a bare address, which is the point: a
// subscription names far fewer domestic services than exist, and the ones it
// leaves out arrive here as a fake address the kernel has already mapped back to
// a name.
const chinaDirectRule = "RULE-SET," + chinaProviderName + ",DIRECT"

const chinaProviderName = "SmartVPNChina"

// providerHealthCheck is part of every provider definition, and it is off on
// purpose.
//
// The kernel's own check tests every node in the provider on an interval, and
// the only thing it maintains is the per-node `alive` flag that the node list
// reads. The app's patrol measures the same nodes with the same request and
// refreshes that flag itself — one group measurement moves the provider's alive
// count — so leaving the kernel's check on would double the probes sent to the
// subscription's servers to learn nothing more. Every probe is a connection to
// the provider from the user's own address, and a subscription that limits
// connections per user sees this client's traffic arrive twice over.
const providerHealthCheck = "    health-check:\n      enable: false\n"

// testIntervalSec is how often a group measures itself. It is the interval only
// of the automatic group, which is what picks a node when no region is locked;
// when a region is locked the pin is failure-driven, so a slow cadence here
// costs nothing. Each measurement tests every node in the subscription, which is
// the whole cost of the feature.
const testIntervalSec = 300

func generatedConfigGo(options configOptions) string {
	var config strings.Builder
	fmt.Fprintf(&config, "mixed-port: %d\n", options.MixedPort)
	config.WriteString("allow-lan: false\n")
	if !options.ClashRS {
		config.WriteString("tcp-concurrent: true\n")
	}
	config.WriteString("bind-address: 127.0.0.1\n")
	config.WriteString("mode: rule\n")
	config.WriteString("log-level: info\n")
	// Naming the process behind each connection is what makes the connection
	// list readable, and it is also what a process rule needs. The kernel caches
	// the lookup, so the cost is one table read per new connection — where the
	// lookup works at all, which is what the platform decides.
	if !options.ClashRS {
		fmt.Fprintf(&config, "find-process-mode: %s\n", findProcessMode)
	}
	fmt.Fprintf(&config, "external-controller: 127.0.0.1:%d\n", options.ControllerPort)
	fmt.Fprintf(&config, "secret: %s\n", strconv.Quote(options.ControllerSecret))
	config.WriteString("profile:\n  store-selected: true\n")
	// v0.10.12 ignores proxy-server-nameserver-policy.
	if options.ClashRS {
		options.EntryDNS = nil
	}
	if options.TUN {
		if options.ClashRS {
			_, device, mtu, _ := configTUNFacts()
			fmt.Fprintf(&config, "tun:\n  enable: true\n  device: %s\n  route-all: true\n  dns-hijack: true\n  mtu: %d\n", device, mtu)
		} else {
			config.WriteString(tunConfigSection(options.DNSProtection))
		}
		config.WriteString(dnsConfigSectionForKernel(options.ClashRS, options.ProxyServerDomains, options.DNSProtection, options.DomesticWeb, runtime.GOOS == "android", options.dnsRelayAddr))
		config.WriteString(entryDNSSection(options.EntryDNS))
	} else if len(options.EntryDNS) > 0 {
		config.WriteString("dns:\n  enable: true\n" + dnsUpstreamsGo(false, false, "") + entryDNSSection(options.EntryDNS))
	}
	config.Write(options.ChainProxies)
	config.WriteString("proxy-providers:\n")
	providers := make([]string, 0, 2)
	if options.SubscriptionProvider {
		config.WriteString("  SmartVPNSubscription:\n    type: file\n")
		fmt.Fprintf(&config, "    path: %s\n", strconv.Quote(options.ProviderPath))
		config.WriteString(providerHealthCheck)
		providers = append(providers, "SmartVPNSubscription")
	}
	// The nodes pasted by hand get their own provider, which the groups below
	// treat exactly like the subscription: same measuring, same health checks.
	if options.ManualProvider {
		fmt.Fprintf(&config, "  %s:\n    type: file\n", manualProviderName)
		fmt.Fprintf(&config, "    path: %s\n", strconv.Quote(manualProviderPath))
		config.WriteString(providerHealthCheck)
		providers = append(providers, manualProviderName)
	}
	var uses strings.Builder
	for _, provider := range providers {
		fmt.Fprintf(&uses, "\n      - %s", provider)
	}
	config.WriteString("proxy-groups:\n")
	if len(options.ChainProxies) > 0 {
		fmt.Fprintf(&config, "  - name: SmartVPN\n    type: select\n    proxies:\n      - %s\n      - REJECT\n", chainExit)
	} else {
		fmt.Fprintf(&config, "  - name: SmartVPNAuto\n    type: url-test\n    use:%s\n    url: https://www.youtube.com/generate_204\n    interval: %d\n    tolerance: 100\n", uses.String(), testIntervalSec)
		fmt.Fprintf(&config, "  - name: SmartVPNFallback\n    type: fallback\n    use:%s\n    url: https://www.youtube.com/generate_204\n    interval: %d\n", uses.String(), testIntervalSec)
		fmt.Fprintf(&config, "  - name: SmartVPN\n    type: select\n    use:%s\n    proxies:\n      - SmartVPNAuto\n      - SmartVPNFallback\n      - REJECT\n", uses.String())
	}
	privateTUN := options.TUN && options.active()
	if options.ChinaDirect && (!privateTUN || options.DomesticWeb) {
		config.WriteString("rule-providers:\n")
		fmt.Fprintf(&config, "  %s:\n    type: file\n    behavior: ipcidr\n    format: text\n    path: %s\n",
			chinaProviderName, strconv.Quote(filepath.Join("providers", chinaIPListFile)))
	}
	config.WriteString("rules:\n")
	if options.dnsRelayAddr != "" {
		// The service connects through the kernel's mixed port. Pin the two
		// DoH destinations ahead of subscription and domestic routing rules.
		for _, ip := range dohIPs {
			fmt.Fprintf(&config, "  - AND,((NETWORK,tcp),(DST-PORT,443),(IP-CIDR,%s/32,no-resolve)),SmartVPN\n", ip)
		}
	}
	// v0.10.12 attempts SOCKS UDP even when the node declares udp: false.
	// Fail closed before process exceptions and imported rules in protected mode.
	if options.ClashRS && options.TUN && options.WebRTCProtection {
		config.WriteString("  - NETWORK,udp,REJECT\n")
	}
	if options.TUN && findProcessMode == "always" {
		for _, name := range options.GameBypass {
			if rule := windowsGameRule(name); rule != "" {
				fmt.Fprintf(&config, "  - %s\n", strconv.Quote(rule))
			}
		}
	}
	decided, proxied := splitRulesByTarget(options.Rules)
	if privateTUN {
		// Retain explicit blocks. Even if the selected node skips UDP rules,
		// the final REJECT prevents Mihomo's implicit DIRECT fallback.
		for _, rule := range decided {
			if ruleTarget(rule) == "REJECT" {
				fmt.Fprintf(&config, "  - %s\n", strconv.Quote(rule))
			}
		}
		if !options.WebRTCProtection {
			fmt.Fprintf(&config, "  - %s\n", strconv.Quote(quicRejectRule))
		}
		if options.SourceRuleOrder {
			for _, rule := range proxied {
				fmt.Fprintf(&config, "  - %s\n", strconv.Quote(rule))
			}
		}
		if options.DomesticWeb && options.ChinaDirect {
			writeDomesticTCPRulesGo(&config, options)
		}
		if options.ClashRS && !options.WebRTCProtection && len(options.ChainProxies) == 0 {
			// A TCP-only node in clash-rs still attempts SOCKS UDP. Preserve explicit DIRECT routes for unprotected UDP.
			for _, r := range options.Rules {
				if tcp, ok := domesticDirectTCPRule(r); ok {
					fmt.Fprintf(&config, "  - %s\n", strconv.Quote(strings.Replace(tcp, "(NETWORK,tcp)", "(NETWORK,udp)", 1)))
				}
			}
		}
		config.WriteString("  - MATCH,SmartVPN\n")
		if options.WebRTCProtection || len(options.ChainProxies) > 0 {
			config.WriteString("  - MATCH,REJECT\n")
		}
		return config.String()
	}
	if options.SourceRuleOrder {
		for _, rule := range options.Rules {
			fmt.Fprintf(&config, "  - %s\n", strconv.Quote(rule))
		}
		if options.ChinaDirect {
			fmt.Fprintf(&config, "  - %s\n", strconv.Quote(chinaDirectRule))
		}
		fmt.Fprintf(&config, "  - %s\n", strconv.Quote(quicRejectRule))
		config.WriteString("  - MATCH,SmartVPN\n")
		if len(options.ChainProxies) > 0 {
			config.WriteString("  - MATCH,REJECT\n")
		}
		return config.String()
	}
	for _, rule := range decided {
		fmt.Fprintf(&config, "  - %s\n", strconv.Quote(rule))
	}
	if options.ChinaDirect {
		fmt.Fprintf(&config, "  - %s\n", strconv.Quote(chinaDirectRule))
	}
	fmt.Fprintf(&config, "  - %s\n", strconv.Quote(quicRejectRule))
	for _, rule := range proxied {
		fmt.Fprintf(&config, "  - %s\n", strconv.Quote(rule))
	}
	config.WriteString("  - MATCH,SmartVPN\n")
	if len(options.ChainProxies) > 0 {
		config.WriteString("  - MATCH,REJECT\n")
	}
	return config.String()
}

func isSafeRuleValue(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x21 || b > 0x7e || b == '(' || b == ')' || b == '"' || b == '\\' || b == ',' {
			return false
		}
	}
	return true
}

func domesticDirectTCPRule(rule string) (string, bool) {
	parts := strings.Split(rule, ",")
	if len(parts) != 3 && len(parts) != 4 {
		return "", false
	}
	kind := strings.TrimSpace(parts[0])
	switch kind {
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6":
	default:
		return "", false
	}
	value := strings.TrimSpace(parts[1])
	if !isSafeRuleValue(value) || strings.TrimSpace(parts[2]) != "DIRECT" {
		return "", false
	}
	if len(parts) == 4 {
		if strings.TrimSpace(parts[3]) != "no-resolve" {
			return "", false
		}
		return "AND,((NETWORK,tcp),(" + kind + "," + value + ",no-resolve)),DIRECT", true
	}
	return "AND,((NETWORK,tcp),(" + kind + "," + value + ")),DIRECT", true
}

// A game login or a corporate service can use TCP on a port other than 443.
// Keep direct rules TCP-only. Known domestic services can be matched by name
// before an overseas resolver or CDN address determines the route.
func writeDomesticTCPRulesGo(config *strings.Builder, options configOptions) {
	add := func(rule string) { fmt.Fprintf(config, "  - %s\n", strconv.Quote(rule)) }
	if options.DNSProtection {
		add("AND,((NETWORK,tcp),(DST-PORT,53/853)),SmartVPN")
		// Common domestic encrypted resolvers also accept HTTPS on port 443.
		for _, domain := range []string{"dns.alidns.com", "doh.pub"} {
			add("DOMAIN-SUFFIX," + domain + ",SmartVPN")
		}
		for _, address := range []string{"223.5.5.5", "223.6.6.6", "120.53.53.53", "1.12.12.12"} {
			add("IP-CIDR," + address + "/32,SmartVPN,no-resolve")
		}
	}
	if options.WebRTCProtection {
		add("AND,((NETWORK,tcp),(DST-PORT,3478/5349/19302)),SmartVPN")
	}
	// Bilibili's media hosts (e.g. i0.hdslb.com) can resolve to overseas
	// edges. Let the domestic IP rule below select DIRECT only for local
	// edges; overseas edges must reach the final proxy rule.
	for _, domain := range []string{"mihoyo.com", "biligame.com", "bilibili.com", "gov.cn"} {
		add("AND,((NETWORK,tcp),(DOMAIN-SUFFIX," + domain + ")),DIRECT")
	}
	// Preserve subscription DIRECT rules as TCP-only so domestic/CDN rules still
	// work under DomesticWeb without leaking UDP, STUN, or DNS.
	for _, rule := range options.Rules {
		if tcpRule, ok := domesticDirectTCPRule(rule); ok {
			add(tcpRule)
		}
	}
	// Only browsers should retry domestic HTTP/3 over TCP on Windows. A game can
	// also use UDP:443 and must still be able to reach its UDP-capable node.
	// On Android, bypassed games are already excluded at VpnService UID level.
	if options.WebRTCProtection {
		if findProcessMode == "always" {
			add("AND,((NETWORK,udp),(DST-PORT,443),(OR,((PROCESS-NAME,chrome.exe),(PROCESS-NAME,msedge.exe),(PROCESS-NAME,firefox.exe),(PROCESS-NAME,brave.exe),(PROCESS-NAME,opera.exe),(PROCESS-NAME,vivaldi.exe),(PROCESS-NAME,arc.exe))),(RULE-SET," + chinaProviderName + ")),REJECT")
		} else if runtime.GOOS == "android" {
			add("AND,((NETWORK,udp),(DST-PORT,443),(RULE-SET," + chinaProviderName + ")),REJECT")
		}
	}
	add("AND,((NETWORK,tcp),(RULE-SET," + chinaProviderName + ")),DIRECT")
}

func dnsConfigSectionGo(proxyServerFilters []string, strict, domesticWeb, android bool, relayAddr string) string {
	return dnsConfigSectionForKernel(false, proxyServerFilters, strict, domesticWeb, android, relayAddr)
}
func dnsConfigSectionForKernel(clashRS bool, proxyServerFilters []string, strict, domesticWeb, android bool, relayAddr string) string {
	header := "dns:\n  enable: true\n  ipv6: false\n" + dnsListenLine
	// Android apps can retain an answer when their UID moves outside the VPN.
	// Return routable addresses even before the user creates a bypass list.
	if android {
		return header + "  enhanced-mode: redir-host\n" + dnsUpstreamsForKernel(clashRS, strict, domesticWeb, relayAddr)
	}
	var filters strings.Builder
	for _, entry := range proxyServerFilters {
		fmt.Fprintf(&filters, "    - %s\n", strconv.Quote(entry))
	}
	return header + fmt.Sprintf(`  enhanced-mode: fake-ip
  fake-ip-range: %s
  fake-ip-filter:
    - "*.lan"
    - "*.local"
    - "*.arpa"
    - "+.msftncsi.com"
    - "www.msftconnecttest.com"
    - "time.*.com"
%s`, tunFakeIPv4Range, filters.String()) + dnsUpstreamsForKernel(clashRS, strict, domesticWeb, relayAddr)
}

//go:embed domestic_dns_suffixes.txt
var domesticDNSSuffixes string

const parallelDoHServers = "    - \"https://1.1.1.1/dns-query#SmartVPN\"\n    - \"https://8.8.8.8/dns-query#SmartVPN\"\n"

const (
	entryDoH  = "https://223.5.5.5/dns-query"
	entryUDP1 = "223.5.5.5"
	entryUDP2 = "119.29.29.29"
)

func dnsUpstreamsGo(strict, domesticWeb bool, relayAddr string) string {
	return dnsUpstreamsForKernel(false, strict, domesticWeb, relayAddr)
}
func dnsUpstreamsForKernel(clashRS bool, strict, domesticWeb bool, relayAddr string) string {
	if strict {
		// Node hostnames must resolve before the tunnel can carry DNS. Using IP
		// endpoints avoids a separate system lookup of a resolver hostname.
		// Domestic direct DoH policy is enabled only when DomesticWeb is active,
		// so pure DNS-protection mode never resolves domestic CDN IPs only to
		// route them through an overseas proxy.
		var policy strings.Builder
		if domesticWeb {
			policy.WriteString("  nameserver-policy:\n")
			for _, suffix := range strings.Fields(domesticDNSSuffixes) {
				// Mihomo's +. wildcard includes the apex and subdomains, but
				// never matches a similarly named domain under another suffix.
				// clash-rs v0.10.12 accepts one resolver per suffix.
				if clashRS {
					fmt.Fprintf(&policy, "    \"+.%s\": \"https://223.5.5.5/dns-query#DIRECT\"\n", suffix)
				} else {
					fmt.Fprintf(&policy, "    \"+.%s\": [\"https://223.5.5.5/dns-query#DIRECT\", \"https://223.6.6.6/dns-query#DIRECT\"]\n", suffix)
				}
			}
		}
		servers := parallelDoHServers
		if relayAddr != "" {
			servers = fmt.Sprintf("    - \"tcp://%s\"\n", relayAddr)
		}
		return policy.String() + `  proxy-server-nameserver:
    - ` + entryDoH + `
  default-nameserver:
    - https://223.5.5.5/dns-query
  nameserver:
` + servers
	}
	return `  proxy-server-nameserver:
    - ` + entryUDP1 + `
    - ` + entryUDP2 + `
  default-nameserver:
    - 223.6.6.6
    - 119.29.29.29
  nameserver:
    - https://doh.pub/dns-query
    - https://dns.alidns.com/dns-query
`
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port, nil
}

func analyzeSubscriptionPayload(body []byte) payloadReport {
	q := subscriptionHTTPServiceRequest{Op: "analyze"}
	e := newAppSubscriptionHTTPEffects(nil, q)
	e.payload = body
	r := runSubscriptionHTTPWithEffects(q, e)
	if r.Err != nil {
		panic(r.Err)
	}
	return subscriptionHTTPPayload(r.Analysis)
}

func readCachedVMessDetails(path string) map[string]vmessDetails {
	result := map[string]vmessDetails{}
	body, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	text := strings.TrimSpace(string(body))
	if !hasProxyURI(text) {
		if decoded, decodeErr := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), "")); decodeErr == nil {
			text = string(decoded)
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^vmess://([^\s]+)`).FindAllStringSubmatch(text, -1) {
		encoded := match[1]
		decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			decoded, decodeErr = base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
		}
		if decodeErr != nil {
			continue
		}
		var details vmessDetails
		if json.Unmarshal(decoded, &details) == nil && details.Name != "" {
			result[details.Name] = details
		}
	}
	return result
}

func hasProxyURIGo(text string) bool {
	return regexp.MustCompile(`(?m)^(vmess|vless|ss|ssr|trojan|hysteria2|hy2|hysteria|tuic)://`).MatchString(text)
}

// Providers prepend notices to the node list. These markers are worded so they
// cannot appear in a real node name; matching is deliberately narrow to avoid
// hiding usable nodes.
func isInformationalNodeGo(name string) bool {
	for _, marker := range []string{
		"若您未看见节点", "客户端版本低", "获取最新客户端", "请到", "永久网址",
		"剩余流量", "过期时间", "到期时间", "距离下次重置", "重置剩余",
		"网站客服", "联系网站客服", "点击订阅", "点击更新", "官方网址", "续费",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func validateSubscriptionURL(raw string) error {
	q := nodeFrontRequest{Op: "validate", Path: raw}
	fx := &appNodeFrontEffects{q: q}
	return runSelectedNodeFront(q, fx).Err
}

// Separate wrapper keeps URL parsing limited to syntax checks; Mihomo performs the fetch.
func neturlParse(raw string) (*url.URL, error) { return url.ParseRequestURI(raw) }

func decodeJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r, 64*1024))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }

// Borrowed-lock variants are explicit: callers already own app.mu.
func (a *app) listNodesLocked() ([]proxyNode, error) {
	r := runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "list", Borrowed: true}, nil)
	return r.Nodes, r.Err
}
func (a *app) waitForNodesLocked(timeout time.Duration) error {
	return runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "wait", Borrowed: true, Timeout: timeout}, nil).Err
}
func (a *app) groupDelaysForURLLocked(target string, timeout time.Duration) (map[string]int, error) {
	r := runAppNodeQueryService(a, nodeQueryServiceRequest{Op: "delay", Borrowed: true, Target: target, Timeout: timeout}, nil)
	return r.Delays, r.Err
}

func (a *app) groupDelaysLocked(timeout time.Duration) (map[string]int, error) {
	return a.groupDelaysForURLLocked(patrolTargetPrimary, timeout)
}
