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
	"sort"
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
	// The sites the path monitor checks, and how often.
	PathChecks pathCheckSettings `json:"pathChecks,omitempty"`
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
	mu              sync.Mutex
	home            string
	settings        settings
	kernel          kernelRunner
	dnsRelay        *adaptiveDNS
	started         time.Time
	proxyOn         bool
	ctrlSecret      string
	mixedPort       int
	ctrlPort        int
	shutdown        chan struct{}
	cachedNodes     []proxyNode
	store           *store
	autoMergeCancel chan struct{}
	healthParams    healthParams
	health          map[string]nodeHealth
	regions         map[string]regionRecord
	regionJob       *regionJob
	patrolCancel    chan struct{}
	blocked         bool
	blockReason     string
	lastSwitch      time.Time
	// Set when a sweep condemned every node of the locked region while a real
	// request still got through, which is a measurement problem rather than a
	// dead region. It says so on the connection page until a sweep measures
	// something again.
	sweepNote string
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
	apiPort      int
	lockedRegion string
	lockedNode   string
	elevated     bool
	// The previous counter reading, which turns two totals into a rate.
	lastTraffic          trafficSample
	trafficHistoryCancel chan struct{}
	trafficHistoryError  string
	// The nodes pasted by hand, which live in their own provider file.
	manualNodes []manualNode
	// The saved subscriptions; the active one is settings.SubscriptionURL.
	subscriptions []subscriptionEntry
	// True while the profile in place came from a subscription's cached copy
	// rather than from a fresh download.
	profileFromCache bool
	// Where the executable sits, which is where a packaged copy keeps the kernel,
	// and whether this copy keeps its data beside it.
	bundleDir string
	portable  bool
	// The path monitor: its timer, the last round's answers, and a guard so two
	// rounds never overlap.
	pathCancel chan struct{}
	pathStatus pathStatus
	pathBusy   bool
	// What the configuration in use says about domestic traffic going direct,
	// and the download the tests replace with a fixture.
	chinaDirect   chinaIPStatus
	chinaFetch    func() ([]byte, string, error)
	chinaFetching bool
	chinaProbed   bool
	chinaRuleOK   bool
	// Whether the configuration the kernel was started with carries the rule.
	chinaDirectActive bool
}

type bootstrap struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	// Set instead of a port when this launch decided not to serve at all, so
	// the window can say why rather than waiting for a line that never comes.
	Error string `json:"error,omitempty"`
}

type proxyNode struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Network  string `json:"network,omitempty"`
	TLS      bool   `json:"tls"`
	WsHost   bool   `json:"wsHostConfigured"`
	WsPath   bool   `json:"wsPathConfigured"`
	Alive    bool   `json:"alive"`
	Selected bool   `json:"selected"`
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
	a := &app{home: home, bundleDir: bundleDir, portable: portable, shutdown: make(chan struct{}, 1)}
	a.elevated = elevated
	a.healthParams = defaultHealthParams()
	a.health = map[string]nodeHealth{}
	a.regions = map[string]regionRecord{}
	a.loadSettings()
	a.lockedRegion = a.settings.LockedRegion
	a.lockedNode = a.settings.LockedNode
	a.sweepStaleConfigs()
	// A portable copy runs the kernel where it lies, so nothing is copied. A copy
	// that carries no marker but does carry one beside the executable gets it
	// placed in its profile instead.
	if !portable {
		a.adoptBundledFiles(bundleDir)
	}
	if opened, err := openStore(filepath.Join(home, "smartvpn.db")); err != nil {
		log.Printf("could not open the local database: %v", err)
	} else {
		a.store = opened
		if params, err := a.store.loadHealthParams(); err != nil {
			log.Printf("%v", err)
		} else {
			if runtime.GOOS == "android" {
				if params.ConnectTimeoutMs < 5000 {
					params.ConnectTimeoutMs = 6000
				}
				if params.FailureThreshold < 3 {
					params.FailureThreshold = 3
				}
			}
			a.healthParams = params
		}
		if health, err := a.store.loadHealth(); err != nil {
			log.Printf("could not load node health: %v", err)
		} else {
			a.health = health
		}
		if regions, err := a.store.loadRegions(); err != nil {
			log.Printf("could not load exit regions: %v", err)
		} else {
			a.regions = regions
		}
		a.loadManualNodesLocked()
		a.loadSubscriptionsLocked()
	}
	return a
}

// prepare finishes what newService started. It runs once this launch has been
// decided to be the one that serves, because the first thing it does can take
// the machine's proxy setting away from a copy that is still using it.
func (a *app) prepare(redactor *logRedactor) {
	// Leave the machine's proxy alone while another copy is serving it. This
	// restore exists to clean up after a run that is gone — including this app's
	// own previous run — and taking the setting away from a copy that is using it
	// would break that copy's connection.
	if a.proxyServedByAnotherInstance() {
		log.Printf("another SmartVPN is serving the Windows proxy; leaving it as it is")
	} else if err := a.setSystemProxy(false); err != nil {
		log.Printf("could not recover previous system proxy settings: %v", err)
	}
	// The redactor needs the URLs now that the settings and the saved list have
	// been read, and the kernel's log is cleaned before the kernel can be started.
	a.redactor = redactor
	redactor.Set(a.subscriptionSecrets())
	a.scrubKernelLog()

	// The list of the country's own addresses is fetched in the background: a
	// connect must never wait on a download, and a machine with no list yet
	// simply routes everything through the proxy as it did before.
	a.startChinaIPRefresh(chinaListIfOverdue)
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err == nil && listener.Addr().(*net.TCPAddr).Port == a.settings.SocksPort {
		reserved := listener
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
		_ = reserved.Close()
	}
	if err != nil {
		return 0, nil, err
	}
	server := &http.Server{Handler: a.routes(token), ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("local API stopped: %v", err)
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	// The ownership record publishes this port: the machine's proxy setting names
	// the kernel's port, and only this one can answer whether a copy still runs.
	a.apiPort = port
	return port, server, nil
}

// routes is the whole local API. Every route but the identity one asks for the
// token; a platform adds the ones only it can serve.
func (a *app) routes(token string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/identify", a.identify)
	mux.HandleFunc("GET /api/state", a.authorize(token, a.state))
	mux.HandleFunc("PUT /api/settings", a.authorize(token, a.updateSettings))
	mux.HandleFunc("POST /api/connect", a.authorize(token, a.connect))
	mux.HandleFunc("POST /api/disconnect", a.authorize(token, a.disconnect))
	mux.HandleFunc("POST /api/proxy/reapply", a.authorize(token, a.reapplyProxy))
	mux.HandleFunc("GET /api/nodes", a.authorize(token, a.nodes))
	mux.HandleFunc("GET /api/nodes/latency", a.authorize(token, a.nodeLatency))
	mux.HandleFunc("POST /api/nodes/fetch", a.authorize(token, a.fetchNodes))
	mux.HandleFunc("GET /api/diagnostics/subscription", a.authorize(token, a.subscriptionDiagnostics))
	mux.HandleFunc("POST /api/nodes/refresh", a.authorize(token, a.refreshNodes))
	mux.HandleFunc("PUT /api/nodes/select", a.authorize(token, a.selectNode))
	mux.HandleFunc("POST /api/shutdown", a.authorize(token, a.shutdownService))
	mux.HandleFunc("POST /api/direct/refresh", a.authorize(token, a.refreshChinaDirect))
	mux.HandleFunc("GET /api/latency", a.authorize(token, a.latency))
	mux.HandleFunc("POST /api/diagnose", a.authorize(token, a.diagnose))
	mux.HandleFunc("POST /api/speedtest", a.authorize(token, a.speedTest))
	mux.HandleFunc("GET /api/path-checks", a.authorize(token, a.pathChecks))
	mux.HandleFunc("PUT /api/path-checks", a.authorize(token, a.updatePathChecks))
	mux.HandleFunc("POST /api/path-checks/run", a.authorize(token, a.runPathChecks))
	mux.HandleFunc("POST /api/path-checks/recovery/cancel", a.authorize(token, a.cancelPathRecovery))
	mux.HandleFunc("GET /api/traffic", a.authorize(token, a.traffic))
	mux.HandleFunc("GET /api/traffic/history", a.authorize(token, a.trafficHistory))
	mux.HandleFunc("GET /api/nodes/manual", a.authorize(token, a.listManualNodes))
	mux.HandleFunc("PUT /api/nodes/chain/profile", a.authorize(token, a.saveChainProfile))
	mux.HandleFunc("GET /api/nodes/probes", a.authorize(token, a.listProbeResults))
	mux.HandleFunc("POST /api/nodes/chain/probe", a.authorize(token, a.probeChain))
	mux.HandleFunc("PUT /api/nodes/chain", a.authorize(token, a.updateChain))
	mux.HandleFunc("POST /api/nodes/manual", a.authorize(token, a.addManualNode))
	mux.HandleFunc("DELETE /api/nodes/manual", a.authorize(token, a.deleteManualNode))
	mux.HandleFunc("GET /api/subscriptions", a.authorize(token, a.listSubscriptions))
	mux.HandleFunc("POST /api/subscriptions/activate", a.authorize(token, a.activateSubscription))
	mux.HandleFunc("POST /api/subscriptions/add", a.authorize(token, a.addSubscription))
	mux.HandleFunc("POST /api/subscriptions/update", a.authorize(token, a.updateSubscription))
	mux.HandleFunc("POST /api/subscriptions/merge", a.authorize(token, a.mergeSubscriptions))
	mux.HandleFunc("POST /api/subscriptions/refresh", a.authorize(token, a.refreshSingleSubscription))
	mux.HandleFunc("POST /api/subscriptions/quota", a.authorize(token, a.refreshSubscriptionQuota))
	mux.HandleFunc("POST /api/subscriptions/auto-merge", a.authorize(token, a.setAutoMergeHours))
	mux.HandleFunc("PUT /api/subscriptions", a.authorize(token, a.renameSubscription))
	mux.HandleFunc("DELETE /api/subscriptions", a.authorize(token, a.deleteSubscription))
	mux.HandleFunc("GET /api/regions", a.authorize(token, a.regionReport))
	mux.HandleFunc("POST /api/regions/verify", a.authorize(token, a.startRegionVerification))
	mux.HandleFunc("POST /api/region/lock", a.authorize(token, a.lockRegion))
	mux.HandleFunc("GET /api/events", a.authorize(token, a.switchEvents))
	mux.HandleFunc("PUT /api/health/params", a.authorize(token, a.updateHealthParams))
	mux.HandleFunc("POST /api/health/patrol", a.authorize(token, a.patrolNow))
	mux.HandleFunc("GET /api/tun", a.authorize(token, a.tunStatus))
	mux.HandleFunc("POST /api/tun/check", a.authorize(token, a.runTunChecks))
	registerPlatformRoutes(mux, token, a)
	return mux
}

// shutdownAndCleanup stops the kernel and puts back whatever this run changed.
func (a *app) shutdownAndCleanup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopAutoMergeLoopLocked()
	_ = a.stopMihomoLocked()
	a.restoreProxy()
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *app) authorize(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next(w, r)
	}
}

func (a *app) state(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	running := a.kernelRunningLocked()
	proxyEnabled := false
	if running && a.proxyOn {
		var err error
		proxyEnabled, err = a.systemProxyEnabled()
		if err != nil {
			log.Printf("could not check Windows proxy status: %v", err)
		}
	}
	activeNode := ""
	activeRegion := ""
	if running {
		activeNode = a.activeNodeLocked()
		if record, ok := a.regions[activeNode]; ok {
			activeRegion = record.Country
		}
	}
	tunReasonText := tunReason(a.tunUnavailableLocked())
	writeJSON(w, map[string]any{
		"connected":       running,
		"subscriptionUrl": a.settings.SubscriptionURL,
		"mihomoPath":      a.settings.MihomoPath,
		// What will actually be run, which differs from the field above whenever
		// the kernel came with the package.
		"mihomoResolved":   a.mihomoPath(),
		"home":             a.home,
		"portable":         a.portable,
		"proxyEnabled":     proxyEnabled,
		"mixedPort":        a.mixedPort,
		"socksPort":        a.settings.SocksPort,
		"startedAt":        a.started,
		"selectedNode":     a.selectedChoice(),
		"activeNode":       activeNode,
		"chain":            a.settings.Chain,
		"chains":           a.settings.Chains,
		"activeRegion":     activeRegion,
		"lockedRegion":     a.lockedRegion,
		"blocked":          a.blocked,
		"blockReason":      a.blockReason,
		"health":           a.healthSnapshotLocked(),
		"regionJob":        a.regionJobSnapshotLocked(),
		"connectionMode":   a.connectionModeLocked(),
		"strictPrivacy":    a.settings.WebRTCProtection && a.settings.DNSProtection,
		"domesticWeb":      a.settings.DomesticWeb,
		"gameBypass":       a.settings.GameBypass,
		"webRTCProtection": a.settings.WebRTCProtection,
		"dnsProtection":    a.settings.DNSProtection,
		"wintunPath":       a.settings.WintunPath,
		"elevated":         a.elevated,
		"tunAvailable":     tunReasonText == "",
		"tunReason":        tunReasonText,
		// True whenever the traffic is carried by a tunnel rather than by the
		// machine's proxy setting, which is what the window's status row says:
		// the mode itself already names which kind of tunnel it is.
		"tunActive":   running && a.connectionModeLocked() != modeSystemProxy,
		"chinaDirect": a.chinaIPStatusLocked(),
	})
}

func (a *app) reapplyProxy(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() {
		http.Error(w, "connect before enabling the Windows proxy", http.StatusConflict)
		return
	}
	if a.connectionModeLocked() != modeSystemProxy {
		http.Error(w, "this platform has no machine proxy setting to reapply", http.StatusConflict)
		return
	}
	if err := a.setSystemProxy(true); err != nil {
		http.Error(w, "could not re-enable the Windows system proxy", http.StatusInternalServerError)
		return
	}
	a.proxyOn = true
	writeJSON(w, map[string]bool{"ok": true})
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

func windowsGameRule(entry string) string {
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

func normalizeGameBypass(entries []string, android bool) ([]string, error) {
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
			if windowsGameRule(name) == "" {
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
	var patch settingsPatch
	if err := decodeJSON(r.Body, &patch); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.settings
	if patch.SocksPort != nil {
		if runtime.GOOS != "windows" || *patch.SocksPort < 0 || *patch.SocksPort > 65535 {
			http.Error(w, "Windows SOCKS5 端口须为 1–65535，或设为 0 自动分配", http.StatusBadRequest)
			return
		}
		next.SocksPort = *patch.SocksPort
		if next.SocksPort != 0 && next.SocksPort == a.apiPort {
			http.Error(w, "端口已被本地管理服务使用，请选择其他端口", http.StatusConflict)
			return
		}
	}
	if patch.GameBypass != nil {
		names, err := normalizeGameBypass(*patch.GameBypass, runtime.GOOS == "android")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next.GameBypass = names
	}
	if patch.StrictPrivacy != nil {
		next.WebRTCProtection, next.DNSProtection = *patch.StrictPrivacy, *patch.StrictPrivacy
	}
	if patch.DomesticWeb != nil {
		if *patch.DomesticWeb && !next.DomesticWeb {
			next.WebRTCProtection, next.DNSProtection = true, true
		}
		next.DomesticWeb = *patch.DomesticWeb
	}
	if patch.WebRTCProtection != nil {
		next.WebRTCProtection = *patch.WebRTCProtection
	}
	if patch.DNSProtection != nil {
		next.DNSProtection = *patch.DNSProtection
	}
	if patch.ConnectionMode != nil {
		mode := strings.TrimSpace(*patch.ConnectionMode)
		// Which modes exist is the platform's answer, not this route's: the
		// window offers what this platform has, and a mode from the other one is
		// refused rather than stored and quietly ignored.
		if !connectionModeSupported(mode) {
			http.Error(w, "connection mode must be one this platform offers", http.StatusBadRequest)
			return
		}
		next.ConnectionMode = mode
	}
	if patch.WintunPath != nil {
		path := strings.TrimSpace(*patch.WintunPath)
		if path != "" && !strings.EqualFold(filepath.Ext(path), ".dll") {
			http.Error(w, "wintun path must point to a .dll file", http.StatusBadRequest)
			return
		}
		next.WintunPath = path
	}
	if patch.SubscriptionURL != nil {
		address := strings.TrimSpace(*patch.SubscriptionURL)
		if address != "" {
			if err := validateSubscriptionURL(address); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		next.SubscriptionURL = address
	}
	if patch.MihomoPath != nil {
		path := strings.TrimSpace(*patch.MihomoPath)
		if path != "" && !strings.EqualFold(filepath.Ext(path), ".exe") {
			http.Error(w, "Mihomo path must point to an .exe file", http.StatusBadRequest)
			return
		}
		next.MihomoPath = path
	}

	if a.kernelRunningLocked() {
		http.Error(w, "disconnect before changing settings", http.StatusConflict)
		return
	}
	// The lock is in-memory state with a route of its own: keep what it holds now
	// rather than what the stored settings happen to say.
	next.LockedRegion = a.lockedRegion
	next.LockedNode = a.lockedNode
	urlChanged := next.SubscriptionURL != a.settings.SubscriptionURL
	a.settings = next
	if urlChanged && next.SubscriptionURL != mergedSubscriptionURL {
		// A different subscription is a different set of nodes, so nothing that
		// described the old ones may survive it.
		a.clearNodeBoundStateLocked()
		a.profileFromCache = false
		if entry, ok := a.findSubscriptionByURL(next.SubscriptionURL); ok && a.useCachedProfileLocked(entry.ID) == nil {
			a.profileFromCache = true
		} else {
			_ = os.Remove(a.profilePath())
			_ = os.Remove(a.profilePath() + ".source")
			_ = os.Remove(a.profilePath() + ".format")
		}
	}
	// A URL entered here is worth keeping in the saved list.
	a.ensureSubscriptionLocked(a.settings.SubscriptionURL)
	if err := a.saveSettings(); err != nil {
		http.Error(w, "could not save settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *app) connect(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	// Asked before the kernel is started, because the answer is about the
	// machine's proxy setting and not about this connection: a second copy would
	// silently take that setting away from the copy already using it. TUN does
	// not touch the setting, so it is offered as the way out.
	if a.connectionModeLocked() == modeSystemProxy && a.proxyServedByAnotherInstance() {
		http.Error(w, "另一个 SmartVPN 正在使用系统代理（可能是便携版或另一份安装）；"+
			"先在那一份里断开连接，或改用 TUN 模式（TUN 不使用系统代理）", http.StatusConflict)
		return
	}
	if err := a.startMihomoLocked(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	nodes, err := a.listNodes()
	if err != nil || len(nodes) == 0 {
		a.stopMihomoLocked()
		http.Error(w, "subscription returned no selectable nodes; fetch nodes or check the subscription", http.StatusBadGateway)
		return
	}
	choice := a.selectedChoice()
	autoSwitched := false
	if a.settings.Chain.Enabled {
		choice = chainExit
	} else if a.lockedRegion != "" {
		// A locked region must bind the selection to keep failover inside the region.
		nodeExists := false
		if a.lockedNode != "" {
			for _, n := range nodes {
				if n.Name == a.lockedNode {
					nodeExists = true
					break
				}
			}
		}
		if nodeExists {
			choice = a.lockedNode
		} else {
			candidates := candidatesForRegion(a.lockedRegion, a.regions, a.health, time.Now())
			if len(candidates) > 0 {
				choice = candidates[0]
				a.lockedNode = choice
				a.settings.LockedNode = choice
			} else {
				a.stopMihomoLocked()
				http.Error(w, fmt.Sprintf("锁定地区 %s 没有已验证节点；为避免跨地区直连，本次未连接", a.lockedRegion), http.StatusBadGateway)
				return
			}
		}
	} else if a.settings.SelectionMode == "manual" {
		nodeExists := false
		for _, n := range nodes {
			if n.Name == choice {
				nodeExists = true
				break
			}
		}
		if !nodeExists {
			choice = autoGroup
			a.settings.SelectionMode = "auto"
			a.settings.SelectedNode = ""
			autoSwitched = true
		}
	} else if choice == "" {
		choice = autoGroup
	}

	if _, err := a.mihomoRequest(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": choice}); err != nil {
		a.stopMihomoLocked()
		http.Error(w, "could not select the proxy group", http.StatusBadGateway)
		return
	}
	group, err := a.mihomoRequest(http.MethodGet, "/proxies/SmartVPN", nil)
	if err != nil || group["now"] != choice {
		a.stopMihomoLocked()
		http.Error(w, "Mihomo did not activate the proxy group", http.StatusBadGateway)
		return
	}
	selected := choice
	if choice == autoGroup || choice == fallbackGroup {
		if nested, nestedErr := a.mihomoRequest(http.MethodGet, "/proxies/"+choice, nil); nestedErr == nil {
			if name, ok := nested["now"].(string); ok && name != "" {
				selected = name
			}
		}
	}
	if err := a.saveSettings(); err != nil {
		a.stopMihomoLocked()
		http.Error(w, "could not save the selected node", http.StatusInternalServerError)
		return
	}
	for i := range nodes {
		nodes[i].Selected = nodes[i].Name == choice
	}
	a.cachedNodes = nodes
	if err := a.saveCachedNodesLocked(nodes); err != nil {
		a.stopMihomoLocked()
		http.Error(w, "could not save the fetched node list", http.StatusInternalServerError)
		return
	}
	if a.store != nil {
		if err := a.store.upsertNodes(nodes); err != nil {
			log.Printf("could not persist the node list: %v", err)
		}
	}
	// The lock follows the node that is actually in use, so a fresh connection
	// keeps failover inside the region the user just measured.
	a.lockRegionToLocked(selected)
	if !a.settings.Chain.Enabled && a.lockedRegion != "" && a.lockedNode != "" && a.lockedNode != choice {
		// The lock was only just established from the active node, so the group
		// still points at an automatic group that could leave the region.
		if err := a.switchGroupLocked(a.lockedNode); err != nil {
			log.Printf("could not pin the group to %s: %v", a.lockedNode, err)
		} else {
			choice, selected = a.lockedNode, a.lockedNode
		}
	}
	if a.lockedRegion == "" {
		log.Printf("no verified exit region; same-region failover stays off until a region is locked")
	}
	a.blocked = false
	a.blockReason = ""
	if a.connectionModeLocked() == modeSystemProxy {
		if err := a.setSystemProxy(true); err != nil {
			_ = a.setSystemProxy(false)
			a.stopMihomoLocked()
			http.Error(w, "could not set the Windows system proxy", http.StatusInternalServerError)
			return
		}
		a.proxyOn = true
	} else {
		// TUN takes over the default routes, so the system proxy stays untouched.
		a.proxyOn = false
	}
	a.startPatrolLocked()
	a.startPathMonitorLocked()

	// Asynchronously trigger a patrol sweep so latencies and health states update in background
	go a.patrolOnce()

	latency := 0
	if rec, ok := a.health[selected]; ok && rec.LatencyMs > 0 {
		latency = rec.LatencyMs
	}
	writeJSON(w, map[string]any{"ok": true, "selectedNode": selected, "autoSwitched": autoSwitched, "latencyMs": latency, "lockedRegion": a.lockedRegion, "mode": a.connectionModeLocked()})
}

func (a *app) disconnect(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	stopErr := a.dropConnectionLocked()
	if stopErr != nil && !errors.Is(stopErr, os.ErrProcessDone) {
		http.Error(w, "Mihomo could not be stopped", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
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
	a.stopPatrolLocked()
	a.stopPathMonitorLocked()
	stopErr := a.stopMihomoLocked()
	// Only touch the setting when this run actually turned it on; a backup left
	// by a crashed run is recovered at startup instead.
	var proxyErr error
	if a.proxyOn {
		proxyErr = a.setSystemProxy(false)
	}
	a.proxyOn = false
	if stopErr == nil {
		stopErr = proxyErr
	}
	return stopErr
}

func (a *app) shutdownService(w http.ResponseWriter, r *http.Request) {
	a.disconnect(w, r)
	a.shutdown <- struct{}{}
}

func (a *app) latency(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	running := a.kernelRunningLocked()
	a.mu.Unlock()
	if !running {
		http.Error(w, "connect before measuring latency", http.StatusConflict)
		return
	}
	delays, err := a.groupDelays(8 * time.Second)
	if err != nil {
		http.Error(w, "could not measure the current node", http.StatusBadGateway)
		return
	}
	selected := a.activeNodeLocked()
	if selected == "" {
		http.Error(w, "could not read the current node", http.StatusBadGateway)
		return
	}
	if delays[selected] <= 0 {
		http.Error(w, "current node could not reach YouTube; select another node", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"latencyMs": delays[selected], "selectedNode": selected})
}

func chooseReachableNode(nodes []proxyNode, delays map[string]int, preferred string) (string, int) {
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
	query := url.Values{}
	query.Set("url", target)
	query.Set("timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	endpoint := "http://127.0.0.1:" + strconv.Itoa(a.ctrlPort) + "/group/SmartVPN/delay?" + query.Encode()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+a.ctrlSecret)
	client := http.Client{Timeout: timeout + 12*time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Mihomo delay API returned HTTP %d", response.StatusCode)
	}
	delays := map[string]int{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&delays); err != nil {
		return nil, err
	}
	return delays, nil
}

func (a *app) nodes(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	running := a.kernelRunningLocked()
	cached := append([]proxyNode(nil), a.cachedNodes...)
	manual := manualNodeDetails(a.manualNodes)
	selected := a.settings.SelectedNode
	proxyEnabled := a.proxyOn
	a.mu.Unlock()
	if running {
		nodes, err := a.listNodes()
		if err != nil {
			http.Error(w, "could not read Mihomo nodes", http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes, "proxyEnabled": proxyEnabled})
		return
	}
	// Pasted nodes are known without asking the kernel, so they show up in the
	// list as soon as they are added.
	for i := range cached {
		cached[i].Selected = cached[i].Name == selected
	}
	present := map[string]bool{}
	for _, node := range cached {
		present[node.Name] = true
	}
	for _, node := range manual {
		if present[node.Name] {
			continue
		}
		node.Selected = node.Name == selected
		cached = append(cached, node)
	}
	writeJSON(w, map[string]any{"nodes": cached, "proxyEnabled": false})
}

// nodeLatency measures every node in the group once. The node list needs
// comparable numbers for the nodes the health sweep never touches, and Mihomo
// can only be asked for a whole group at a time.
func (a *app) nodeLatency(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() {
		http.Error(w, "connect before measuring node latency", http.StatusConflict)
		return
	}
	timeout := time.Duration(a.healthParams.ConnectTimeoutMs) * time.Millisecond
	delays, err := a.groupDelaysForURL(patrolTargetPrimary, timeout)
	if err != nil {
		http.Error(w, "could not measure the nodes", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"latency": delays})
}

func (a *app) fetchNodes(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		http.Error(w, "use refresh while connected", http.StatusConflict)
		return
	}
	nodes, err := a.fetchProfileLocked()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	format, _ := os.ReadFile(a.profilePath() + ".format")
	writeJSON(w, map[string]any{"nodes": nodes, "proxyEnabled": false, "appliedFormat": strings.TrimSpace(string(format))})
}

func (a *app) subscriptionDiagnostics(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	subscriptionURL := a.settings.SubscriptionURL
	home := a.home
	a.mu.Unlock()
	if subscriptionURL == "" {
		http.Error(w, "set a subscription URL first", http.StatusBadRequest)
		return
	}

	u, err := url.ParseRequestURI(subscriptionURL)
	if err != nil {
		http.Error(w, "invalid subscription URL", http.StatusBadRequest)
		return
	}
	queryKeys := make([]string, 0, len(u.Query()))
	for key := range u.Query() {
		queryKeys = append(queryKeys, key)
	}
	sort.Strings(queryKeys)
	requestURL := subscriptionURL

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := http.Client{Timeout: 20 * time.Second, Transport: transport}
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		http.Error(w, "invalid subscription URL", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", subscriptionUserAgent)
	resp, err := client.Do(req)
	report := subscriptionReport{
		RequestQueryKeys: queryKeys,
		UserAgent:        subscriptionUserAgent,
		FlagAdded:        false,
	}
	if err != nil {
		report.Fetch.Error = "订阅请求失败，请查看网络或订阅状态"
	} else {
		defer resp.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024+1))
		report.Fetch.StatusCode = resp.StatusCode
		report.Fetch.ContentType = resp.Header.Get("Content-Type")
		if len(body) > 10*1024*1024 {
			body = body[:10*1024*1024]
			report.Fetch.Error = "订阅响应超过 10 MB，诊断已截断"
		} else if readErr != nil {
			report.Fetch.Error = "读取订阅响应失败"
		}
		report.Fetch.Bytes = len(body)
		report.Fetch.Payload = analyzeSubscriptionPayload(body)
	}
	cachePath := filepath.Join(home, "providers", "subscription.yaml")
	if cache, readErr := os.ReadFile(cachePath); readErr == nil {
		report.Cache = analyzeSubscriptionPayload(cache)
	}
	if source, readErr := os.ReadFile(cachePath + ".source"); readErr == nil && strings.TrimSpace(string(source)) == subscriptionHash(subscriptionURL) {
		if format, formatErr := os.ReadFile(cachePath + ".format"); formatErr == nil {
			report.AppliedFormat = strings.TrimSpace(string(format))
		}
	}
	writeJSON(w, report)
}

func (a *app) refreshNodes(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() {
		http.Error(w, "connect before refreshing nodes", http.StatusConflict)
		return
	}
	if _, err := a.fetchProfileLocked(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if _, err := a.mihomoRequest(http.MethodPut, "/providers/proxies/SmartVPNSubscription", nil); err != nil {
		http.Error(w, "subscription refresh failed; check the subscription and network", http.StatusBadGateway)
		return
	}
	if err := a.waitForNodes(15 * time.Second); err != nil {
		http.Error(w, "subscription returned no usable nodes", http.StatusBadGateway)
		return
	}
	nodes, err := a.listNodes()
	if err != nil {
		http.Error(w, "could not read Mihomo nodes", http.StatusBadGateway)
		return
	}
	a.cachedNodes = append([]proxyNode(nil), nodes...)
	cacheErr := a.saveCachedNodesLocked(nodes)
	if cacheErr != nil {
		http.Error(w, "could not save the refreshed node list", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"nodes": nodes})
}

func (a *app) selectNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r.Body, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		http.Error(w, "invalid node selection", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	connected := a.kernelRunningLocked()
	if a.settings.Chain.Enabled {
		a.mu.Unlock()
		http.Error(w, "请先关闭链式代理，再选择单个节点", http.StatusConflict)
		return
	}
	if !connected {
		found := body.Name == autoGroup || body.Name == fallbackGroup
		for _, node := range a.cachedNodes {
			if node.Name == body.Name {
				found = true
				break
			}
		}
		if !found {
			a.mu.Unlock()
			http.Error(w, "fetch the subscription nodes before selecting one", http.StatusConflict)
			return
		}
		a.settings.SelectedNode = ""
		switch body.Name {
		case autoGroup:
			a.settings.SelectionMode = "auto"
		case fallbackGroup:
			a.settings.SelectionMode = "fallback"
		default:
			a.settings.SelectionMode = "manual"
			a.settings.SelectedNode = body.Name
			// The lock follows the chosen node whether or not the kernel is up,
			// otherwise a selection made while disconnected would be ignored.
			a.lockRegionToLocked(body.Name)
		}
		for i := range a.cachedNodes {
			a.cachedNodes[i].Selected = a.cachedNodes[i].Name == body.Name
		}
		if err := a.saveSettings(); err != nil {
			a.mu.Unlock()
			http.Error(w, "could not save the pending node selection", http.StatusInternalServerError)
			return
		}
		a.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true, "pending": true})
		return
	}
	a.mu.Unlock()
	nodes, err := a.listNodes()
	if err != nil {
		http.Error(w, "could not read Mihomo nodes", http.StatusBadGateway)
		return
	}
	found := body.Name == autoGroup || body.Name == fallbackGroup
	for _, node := range nodes {
		if node.Name == body.Name {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "node is no longer in the subscription; refresh nodes", http.StatusBadRequest)
		return
	}
	if _, err := a.mihomoRequest(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": body.Name}); err != nil {
		http.Error(w, "Mihomo could not select that node", http.StatusBadGateway)
		return
	}
	a.mu.Lock()
	if body.Name == autoGroup || body.Name == fallbackGroup {
		a.lockRegionToLocked(a.activeNodeLocked())
	} else {
		a.lockRegionToLocked(body.Name)
	}
	a.settings.SelectedNode = ""
	switch body.Name {
	case autoGroup:
		a.settings.SelectionMode = "auto"
	case fallbackGroup:
		a.settings.SelectionMode = "fallback"
	default:
		a.settings.SelectionMode = "manual"
		a.settings.SelectedNode = body.Name
	}
	_ = a.saveSettings()
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *app) listNodes() ([]proxyNode, error) {
	group, err := a.mihomoRequest(http.MethodGet, "/proxies/SmartVPN", nil)
	if err != nil {
		return nil, err
	}
	selected, _ := group["now"].(string)
	// Provider entries carry only what the kernel reports; the rest (TLS, ws
	// details) comes from the files the kernel is reading, by name.
	details := map[string]proxyNode{}
	if profile, readErr := os.ReadFile(a.profilePath()); readErr == nil {
		for _, node := range parseProfileNodes(profile) {
			details[node.Name] = node
		}
	}
	for name, node := range manualNodeDetails(a.manualNodes) {
		details[name] = node
	}

	nodes := make([]proxyNode, 0, 64)
	seen := map[string]bool{}
	// A machine may have a subscription, pasted nodes, or both; a provider that
	// is not part of this configuration simply contributes nothing.
	for _, providerName := range []string{"SmartVPNSubscription", manualProviderName} {
		provider, err := a.mihomoRequest(http.MethodGet, "/providers/proxies/"+providerName, nil)
		if err != nil {
			continue
		}
		rawNodes, _ := provider["proxies"].([]any)
		for _, raw := range rawNodes {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := item["name"].(string)
			if name == "" || seen[name] || isInformationalNode(name) {
				continue
			}
			seen[name] = true
			kind, _ := item["type"].(string)
			alive, _ := item["alive"].(bool)
			extra := details[name]
			network := extra.Network
			if network == "" {
				if rawNetwork, ok := item["network"].(string); ok {
					network = rawNetwork
				}
			}
			nodes = append(nodes, proxyNode{
				Name: name, Type: kind, Network: network, TLS: extra.TLS,
				WsHost: extra.WsHost, WsPath: extra.WsPath, Alive: alive,
				Selected: name == selected, Manual: extra.Manual,
			})
		}
	}
	return nodes, nil
}

func (a *app) waitForNodes(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nodes, err := a.listNodes()
		if err == nil && len(nodes) > 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("the kernel reported no usable nodes")
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
	if resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
		return result, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *app) waitForMihomo(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort)), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("startup timeout")
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
	a.mu.Lock()
	connected := a.kernelRunningLocked()
	a.mu.Unlock()
	writeJSON(w, map[string]any{"app": appName, "connected": connected})
}

func (a *app) loadSettings() {
	b, err := os.ReadFile(filepath.Join(a.home, "settings.json"))
	if err == nil {
		_ = json.Unmarshal(b, &a.settings)
		if a.settings.Chains == nil && len(a.settings.Chain.Hops) >= 2 {
			a.settings.Chain.Name = "我的代理链"
			a.settings.Chains = map[string][]string{"我的代理链": a.settings.Chain.Hops}
		}
		if a.settings.StrictPrivacy {
			a.settings.WebRTCProtection, a.settings.DNSProtection = true, true
			a.settings.StrictPrivacy = false
		}
	}
	if a.settings.SubscriptionURL != "" {
		// What is on disk is the platform's credential form of the URL — the URL
		// itself on Windows, a keystore-sealed blob on Android — and everything
		// above this line works with the URL.
		plain, err := openSecret(a.settings.SubscriptionURL)
		if err != nil {
			log.Printf("could not read the stored subscription URL: %v", err)
			plain = ""
		}
		a.settings.SubscriptionURL = plain
	}
	if a.settings.SelectionMode == "" {
		a.settings.SelectionMode = "auto"
	}
	// A configuration saved before the path monitor existed gets its defaults.
	if a.settings.PathChecks.IntervalSec == 0 && len(a.settings.PathChecks.Targets) == 0 {
		a.settings.PathChecks = defaultPathChecks()
	}
	// The kernel's location is not stored here: an empty value means "the copy
	// that came with the package, or the profile", resolved when it is used, so a
	// folder that is moved keeps working.
	a.loadCachedNodes()
}

func (a *app) selectedChoice() string {
	if a.settings.Chain.Enabled {
		return chainExit
	}
	// A locked region owns the selection, so Mihomo's own automatic groups can
	// never pick a node outside the region the user pinned.
	if a.lockedRegion != "" && a.lockedNode != "" {
		return a.lockedNode
	}
	switch a.settings.SelectionMode {
	case "fallback":
		return fallbackGroup
	case "manual":
		return a.settings.SelectedNode
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
	matches, err := filepath.Glob(filepath.Join(a.home, "config-*.yaml"))
	if err != nil {
		return
	}
	own := a.configPath()
	for _, match := range matches {
		if match != own {
			_ = os.Remove(match)
		}
	}
}

func subscriptionHash(subscriptionURL string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(subscriptionURL)))
}

func (a *app) loadCachedNodes() {
	body, err := os.ReadFile(a.cachedNodesPath())
	if err != nil {
		return
	}
	var cache cachedNodeList
	if json.Unmarshal(body, &cache) != nil || cache.SubscriptionHash != subscriptionHash(a.settings.SubscriptionURL) {
		_ = os.Remove(a.cachedNodesPath())
		return
	}
	a.cachedNodes = cache.Nodes
	for i := range a.cachedNodes {
		a.cachedNodes[i].Selected = a.cachedNodes[i].Name == a.settings.SelectedNode
	}
}

func (a *app) saveCachedNodesLocked(nodes []proxyNode) error {
	cache := cachedNodeList{
		SubscriptionHash: subscriptionHash(a.settings.SubscriptionURL),
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
	a.cachedNodes = nil
	_ = os.Remove(a.cachedNodesPath())
}

func (a *app) saveSettings() error {
	stored := a.settings
	sealed, err := sealSecret(stored.SubscriptionURL)
	if err != nil {
		return err
	}
	stored.SubscriptionURL = sealed
	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(a.home, "settings.json"), b, 0600); err != nil {
		return err
	}
	// Every change to the settings goes through here, and the subscription in use
	// is one of the things the log redactor has to know about.
	if a.redactor != nil {
		a.redactor.Set(a.subscriptionSecrets())
	}
	return nil
}

type configOptions struct {
	dnsRelayAddr     string
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

func generatedConfig(options configOptions) string {
	var config strings.Builder
	fmt.Fprintf(&config, "mixed-port: %d\n", options.MixedPort)
	config.WriteString("allow-lan: false\n")
	config.WriteString("bind-address: 127.0.0.1\n")
	config.WriteString("mode: rule\n")
	config.WriteString("log-level: info\n")
	// Naming the process behind each connection is what makes the connection
	// list readable, and it is also what a process rule needs. The kernel caches
	// the lookup, so the cost is one table read per new connection — where the
	// lookup works at all, which is what the platform decides.
	fmt.Fprintf(&config, "find-process-mode: %s\n", findProcessMode)
	fmt.Fprintf(&config, "external-controller: 127.0.0.1:%d\n", options.ControllerPort)
	fmt.Fprintf(&config, "secret: %s\n", strconv.Quote(options.ControllerSecret))
	config.WriteString("profile:\n  store-selected: true\n")
	if options.TUN {
		config.WriteString(tunConfigSection(options.DNSProtection))
		config.WriteString(dnsConfigSection(options.ProxyServerDomains, options.DNSProtection, runtime.GOOS == "android", options.dnsRelayAddr))
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
		if options.DomesticWeb && options.ChinaDirect {
			writeDomesticTCPRules(&config, options)
		}
		config.WriteString("  - MATCH,SmartVPN\n")
		if options.WebRTCProtection || len(options.ChainProxies) > 0 {
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

// A game login or a corporate service can use TCP on a port other than 443.
// Keep direct rules TCP-only. Known domestic services can be matched by name
// before an overseas resolver or CDN address determines the route.
func writeDomesticTCPRules(config *strings.Builder, options configOptions) {
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
	// Only browsers should retry domestic HTTP/3 over TCP. A game can also
	// use UDP:443 and must still be able to reach its UDP-capable node.
	if options.WebRTCProtection && findProcessMode == "always" {
		add("AND,((NETWORK,udp),(DST-PORT,443),(OR,((PROCESS-NAME,chrome.exe),(PROCESS-NAME,msedge.exe),(PROCESS-NAME,firefox.exe))),(RULE-SET," + chinaProviderName + ")),REJECT")
	}
	add("AND,((NETWORK,tcp),(RULE-SET," + chinaProviderName + ")),DIRECT")
}

func dnsConfigSection(proxyServerFilters []string, strict, android bool, relayAddr string) string {
	header := "dns:\n  enable: true\n  ipv6: false\n" + dnsListenLine
	// Android apps can retain an answer when their UID moves outside the VPN.
	// Return routable addresses even before the user creates a bypass list.
	if android {
		return header + "  enhanced-mode: redir-host\n" + dnsUpstreams(strict, relayAddr)
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
%s`, tunFakeIPv4Range, filters.String()) + dnsUpstreams(strict, relayAddr)
}

//go:embed domestic_dns_suffixes.txt
var domesticDNSSuffixes string

const parallelDoHServers = "    - \"https://1.1.1.1/dns-query#SmartVPN\"\n    - \"https://8.8.8.8/dns-query#SmartVPN\"\n"

func dnsUpstreams(strict bool, relayAddr string) string {
	if strict {
		// Node hostnames must resolve before the tunnel can carry DNS. Using IP
		// endpoints avoids a separate system lookup of a resolver hostname.
		var policy strings.Builder
		policy.WriteString("  nameserver-policy:\n")
		for _, suffix := range strings.Fields(domesticDNSSuffixes) {
			// Mihomo's +. wildcard includes the apex and subdomains, but
			// never matches a similarly named domain under another suffix.
			fmt.Fprintf(&policy, "    \"+.%s\": [\"https://223.5.5.5/dns-query#DIRECT\", \"https://223.6.6.6/dns-query#DIRECT\"]\n", suffix)
		}
		servers := parallelDoHServers
		if relayAddr != "" {
			servers = fmt.Sprintf("    - \"tcp://%s\"\n", relayAddr)
		}
		return policy.String() + `  proxy-server-nameserver:
    - https://223.5.5.5/dns-query
  default-nameserver:
    - https://223.5.5.5/dns-query
  nameserver:
` + servers
	}
	return `  proxy-server-nameserver:
    - 223.5.5.5
    - 119.29.29.29
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
	report := payloadReport{Present: len(body) > 0, Bytes: len(body), UriCounts: map[string]int{}, Networks: map[string]int{}, TlsModes: map[string]int{}}
	report.Selectable = len(parseProfileNodes(body))
	if len(body) == 0 {
		report.Format = "empty"
		return report
	}
	text := strings.TrimSpace(string(body))
	report.Format = "unknown"
	if strings.HasPrefix(text, "proxies:") || strings.Contains(text, "\nproxies:") {
		report.Format = "clash-yaml"
	} else if strings.HasPrefix(text, "{") {
		report.Format = "singbox-json"
	} else if hasProxyURI(text) {
		report.Format = "uri-list"
	} else {
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
		if err == nil {
			text = strings.TrimSpace(string(decoded))
			if hasProxyURI(text) {
				report.Format = "base64-uri-list"
			} else if strings.HasPrefix(text, "proxies:") || strings.Contains(text, "\nproxies:") {
				report.Format = "base64-clash-yaml"
			} else if strings.HasPrefix(text, "{") {
				report.Format = "base64-singbox-json"
			}
		}
	}
	for _, scheme := range []string{"vmess", "vless", "ss", "ssr", "trojan", "hysteria2", "tuic"} {
		report.UriCounts[scheme] = len(regexp.MustCompile(`(?m)^`+scheme+`://`).FindAllStringIndex(text, -1))
	}
	if strings.Contains(report.Format, "clash-yaml") {
		for _, node := range parseProfileNodes(body) {
			if strings.EqualFold(node.Type, "vmess") {
				report.ValidVmess++
			}
			network := node.Network
			if network == "" {
				network = "tcp/default"
			}
			report.Networks[network]++
			if node.TLS {
				report.TlsModes["tls"]++
			} else {
				report.TlsModes["none"]++
			}
			if node.WsHost {
				report.WsHostSet++
			}
			if node.WsPath {
				report.WsPathSet++
			}
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^vmess://([^\s]+)`).FindAllStringSubmatch(text, -1) {
		encoded := match[1]
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
		}
		if err != nil {
			continue
		}
		var details vmessDetails
		if json.Unmarshal(decoded, &details) != nil {
			continue
		}
		report.ValidVmess++
		if isInformationalNode(details.Name) {
			report.InfoEntries++
		}
		network := details.Net
		if network == "" {
			network = "tcp/default"
		}
		report.Networks[network]++
		tls := details.TLS
		if tls == "" {
			tls = "none"
		}
		report.TlsModes[tls]++
		if network == "ws" {
			if details.Host != "" {
				report.WsHostSet++
			}
			if details.Path != "" {
				report.WsPathSet++
			}
		}
	}
	return report
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

func hasProxyURI(text string) bool {
	return regexp.MustCompile(`(?m)^(vmess|vless|ss|ssr|trojan|hysteria2|hy2|hysteria|tuic)://`).MatchString(text)
}

// Providers prepend notices to the node list. These markers are worded so they
// cannot appear in a real node name; matching is deliberately narrow to avoid
// hiding usable nodes.
func isInformationalNode(name string) bool {
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("subscription URL must be an HTTP(S) URL")
	}
	if raw == mergedSubscriptionURL {
		return nil
	}
	if isInlineSubscription(raw) {
		return nil
	}
	remoteURLs, inlineLines := categorizeSubscriptionInput(raw)
	if len(remoteURLs) == 0 && len(inlineLines) == 0 {
		return errors.New("subscription URL must be an HTTP(S) URL")
	}
	for _, uStr := range remoteURLs {
		u, err := neturlParse(uStr)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return errors.New("subscription URL must be an HTTP(S) URL")
		}
	}
	return nil
}

// Separate wrapper keeps URL parsing limited to syntax checks; Mihomo performs the fetch.
func neturlParse(raw string) (*url.URL, error) { return url.ParseRequestURI(raw) }

func decodeJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r, 64*1024))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
