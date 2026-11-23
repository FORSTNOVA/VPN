package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type diagnoseServiceRequest struct{ Op, ProxyAddress string }
type diagnoseServiceState struct {
	Chain                   bool
	Choice                  string
	DetailsCount, STUNCount int
	SiteNames               []string
}
type diagnoseQualityInput struct {
	Valid, Hosting, Proxy, VPN, Tor, Anonymous bool
	Lower                                      string
}
type diagnoseServiceResult struct {
	Status    int
	Message   string
	Err       error
	Temporary bool
	Report    diagnoseReport
	Egress    egressFacts
	Quality   ipQualityFacts
	DNS       dnsFacts
	Mapped    string
	Elapsed   int64
}
type diagnoseEffect struct {
	Indices                            []int
	Kind, Name, Path, Address, Message string
	Index, Status                      int
	Report                             diagnoseReport
}
type diagnoseReply struct {
	State              *diagnoseServiceState
	Err                error
	Bool               bool
	DNSMatch, UDPMatch bool
	Value              any
	Names              []string
	Address            string
	Egress             egressFacts
	Quality            diagnoseQualityInput
	DNS                dnsFacts
	Relay              udpRelayResult
	Mapped             string
	Elapsed            int64
	Sites              []siteCheckResult
}
type diagnoseServiceEffects interface {
	Call(diagnoseEffect) diagnoseReply
	Release()
}

func cloneDiagnoseServiceState(s *diagnoseServiceState) *diagnoseServiceState {
	if s == nil {
		return nil
	}
	v := *s
	if s.SiteNames != nil {
		v.SiteNames = append([]string{}, s.SiteNames...)
	}
	return &v
}

type appDiagnoseEffects struct {
	sitesDone                  chan struct{}
	siteResults                []siteCheckResult
	siteFailure                any
	udpTimeout                 time.Duration
	a                          *app
	q                          diagnoseServiceRequest
	owned, borrowed, temporary bool
	proxy                      string
	body, stun                 []byte
	running                    func() bool
	start, stop                func() error
	list                       func() ([]proxyNode, error)
	mihomo                     func(string, string, any) (map[string]any, error)
	exit                       func(bool) exitIPResult
	trace                      func() exitIPResult
	detail                     func(int) ([]byte, egressFacts, error)
	resolver                   func() (dnsFacts, error)
	country                    func(string) (string, error)
	relay                      func() udpRelayResult
	buildSTUN                  func() ([]byte, error)
	exchange                   func(int, []byte) (string, int64, error)
	site                       func(int) siteCheckResult
	respond                    func(int, string, diagnoseReport)
}

func newAppDiagnoseEffects(a *app, q diagnoseServiceRequest, w http.ResponseWriter) *appDiagnoseEffects {
	e := &appDiagnoseEffects{a: a, q: q, borrowed: q.Op == "ensure", proxy: q.ProxyAddress, udpTimeout: udpRelayTimeout, buildSTUN: buildSTUNRequest}
	if a != nil {
		e.running = a.kernelRunningLocked
		e.start = a.startMihomoLocked
		e.stop = a.stopMihomoLocked
		e.list = a.listNodesLocked
		e.mihomo = a.mihomoRequest
	}
	proxyURL := func() *url.URL { return &url.URL{Scheme: "http", Host: e.proxy} }
	e.exit = func(v6 bool) exitIPResult { return checkExitIP(proxyURL(), exitIPProbeURL(v6), v6) }
	e.trace = func() exitIPResult { return checkCloudflareExitIP(proxyURL(), cloudflareTraceURL) }
	e.detail = func(i int) ([]byte, egressFacts, error) {
		s := egressSources[i]
		b, err := fetchBody(diagnosableClient(e.proxy, 8*time.Second), s.url)
		if err != nil {
			return nil, egressFacts{}, err
		}
		f, err := s.parse(b)
		return b, f, err
	}
	e.resolver = func() (dnsFacts, error) {
		b, err := fetchBody(diagnosableClient(e.proxy, 8*time.Second), "https://edns.ip-api.com/json")
		if err != nil {
			return dnsFacts{}, err
		}
		var v struct {
			DNS struct {
				IP  string `json:"ip"`
				Geo string `json:"geo"`
			} `json:"dns"`
		}
		if json.Unmarshal(b, &v) != nil || v.DNS.IP == "" {
			return dnsFacts{}, errors.New(diagnoseDNSParseMessage())
		}
		return dnsFacts{Resolver: v.DNS.IP, Geo: v.DNS.Geo}, nil
	}
	e.country = func(ip string) (string, error) { return lookupCountry(e.proxy, ip) }
	e.relay = func() udpRelayResult { return probeUDPRelay(e.proxy, udpRelayTarget, udpRelayTimeout) }
	e.exchange = func(i int, request []byte) (string, int64, error) {
		b, elapsed, err := socksUDPExchangeSelected(e.proxy, stunTargets[i], request, func(packet []byte) []byte {
			return selectSTUNRelayReply(packet, request)
		}, e.udpTimeout)
		if err != nil {
			return "", 0, err
		}
		addr, err := parseSTUNAddress(b)
		return addr, elapsed, err
	}
	e.site = func(i int) siteCheckResult { t := checkTargets[i]; return checkSite(proxyURL(), t.name, t.address) }
	e.respond = func(status int, message string, r diagnoseReport) {
		if status != 200 {
			http.Error(w, message, status)
		} else {
			writeJSON(w, r)
		}
	}
	return e
}
func (e *appDiagnoseEffects) snapshot() *diagnoseServiceState {
	s := &diagnoseServiceState{DetailsCount: len(egressSources), STUNCount: len(stunTargets)}
	for _, t := range checkTargets {
		s.SiteNames = append(s.SiteNames, t.name)
	}
	if e.a != nil {
		s.Chain = e.a.chainEnabledLocked()
		s.Choice = e.a.selectedChoice()
	}
	return s
}
func (e *appDiagnoseEffects) stopTemporary() {
	if e.temporary {
		e.temporary = false
		_ = e.stop()
	}
}
func (e *appDiagnoseEffects) Release() {
	defer func() {
		defer func() {
			if e.owned {
				e.owned = false
				e.a.mu.Unlock()
			}
		}()
		e.stopTemporary()
	}()
	e.joinSites()
}
func (e *appDiagnoseEffects) joinSites() []siteCheckResult {
	if e.sitesDone != nil {
		<-e.sitesDone
		e.sitesDone = nil
		failure := e.siteFailure
		e.siteFailure = nil
		if failure != nil {
			panic(failure)
		}
	}
	return e.siteResults
}
func (e *appDiagnoseEffects) startSites(indices []int) {
	if e.sitesDone != nil {
		panic("duplicate sites batch")
	}
	plan := append([]int(nil), indices...)
	e.sitesDone = make(chan struct{})
	done := e.sitesDone
	e.siteResults = make([]siteCheckResult, len(plan))
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		var once sync.Once
		for i, index := range plan {
			wg.Add(1)
			go func(i, index int) {
				defer wg.Done()
				defer func() {
					if v := recover(); v != nil {
						once.Do(func() { e.siteFailure = v })
					}
				}()
				e.siteResults[i] = e.site(index)
			}(i, index)
		}
		wg.Wait()
	}()
}
func (e *appDiagnoseEffects) Call(q diagnoseEffect) (r diagnoseReply) {
	if q.Kind == "lock" {
		if e.owned || e.borrowed {
			panic("diagnose duplicate lock")
		}
		e.a.mu.Lock()
		e.owned = true
		r.State = e.snapshot()
		return
	}
	if q.Kind == "unlock" {
		if !e.owned {
			panic("diagnose unlock without ownership")
		}
		e.owned = false
		e.a.mu.Unlock()
		return
	}
	if e.a != nil && !e.owned && !e.borrowed {
		panic("diagnose effect outside lock")
	}
	switch q.Kind {
	case "snapshot", "choice":
		r.State = e.snapshot()
	case "running":
		r.Bool = e.running()
	case "start":
		r.Err = e.start()
		if r.Err == nil {
			e.temporary = true
		}
		r.State = e.snapshot()
	case "stop":
		e.temporary = false
		r.Err = e.stop()
	case "list":
		var ns []proxyNode
		ns, r.Err = e.list()
		for _, n := range ns {
			r.Names = append(r.Names, n.Name)
		}
	case "get":
		var g map[string]any
		g, r.Err = e.mihomo(http.MethodGet, q.Path, nil)
		r.Value = g["now"]
	case "put":
		_, r.Err = e.mihomo(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": q.Name})
	case "context":
		e.proxy = net.JoinHostPort("127.0.0.1", strconv.Itoa(e.a.mixedPort))
		r.Address = e.proxy
	case "ipv4":
		r.Address = e.exit(false).Address
	case "trace4":
		r.Address = e.trace().Address
	case "ipv6":
		r.Address = e.exit(true).Address
	case "detail":
		e.body, r.Egress, r.Err = e.detail(q.Index)
	case "quality":
		r.Quality = diagnoseQualityFacts(e.body)
	case "resolver":
		r.DNS, r.Err = e.resolver()
		r.Bool = net.ParseIP(strings.TrimSpace(r.DNS.Resolver)) != nil
	case "country":
		r.Address, r.Err = e.country(q.Address)
	case "relay":
		r.Relay = e.relay()
	case "stun-build":
		e.stun, r.Err = e.buildSTUN()
	case "stun-exchange":
		r.Mapped, r.Elapsed, r.Err = e.exchange(q.Index, e.stun)
	case "sites-start":
		e.startSites(q.Indices)
	case "sites-join":
		r.Sites = e.joinSites()
	case "verdict":
		r.DNSMatch = dnsMatchesExit(q.Report.DNS.Country, q.Report.Egress.CountryCode)
		r.UDPMatch = q.Report.UDP.Mapped != "" && (udpMatchesExit(q.Report.UDP.Mapped, q.Report.Egress.IPv4) || udpMatchesExit(q.Report.UDP.Mapped, q.Report.Egress.IPv6))
	case "response":
		e.respond(q.Status, q.Message, q.Report)
	default:
		panic("unknown diagnose effect: " + q.Kind)
	}
	return
}
func diagnoseSiteBatch(n int, probe func(int) siteCheckResult) []siteCheckResult {
	out := make([]siteCheckResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); out[i] = probe(i) }(i)
	}
	wg.Wait()
	return out
}
func diagnoseQualityFacts(b []byte) diagnoseQualityInput {
	var v struct {
		Connection struct {
			ASN      int `json:"asn"`
			ISP, Org string
		} `json:"connection"`
		Security struct{ Anonymous, Proxy, VPN, Tor, Hosting bool } `json:"security"`
	}
	if json.Unmarshal(b, &v) != nil {
		return diagnoseQualityInput{}
	}
	return diagnoseQualityInput{Valid: true, Hosting: v.Security.Hosting, Proxy: v.Security.Proxy, VPN: v.Security.VPN, Tor: v.Security.Tor, Anonymous: v.Security.Anonymous, Lower: strings.ToLower(v.Connection.ISP + " " + v.Connection.Org)}
}
func (a *app) runDiagnoseService(q diagnoseServiceRequest, w http.ResponseWriter) (out diagnoseServiceResult) {
	responded := false
	defer func() {
		if recover() != nil {
			out = diagnoseServiceResult{Status: 500, Message: "diagnostic operation interrupted", Err: errors.New("diagnostic operation interrupted")}
		}
		if out.Err != nil && w != nil && !responded {
			responded = true
			http.Error(w, "diagnostic operation interrupted", 500)
		}
	}()
	e := newAppDiagnoseEffects(a, q, w)
	original := e.respond
	e.respond = func(status int, msg string, r diagnoseReport) { responded = true; original(status, msg, r) }
	defer e.Release()
	return runSelectedDiagnoseService(q, e)
}

func (e *appDiagnoseEffects) FinalizeDiagnoseResult(q diagnoseServiceRequest, r diagnoseServiceResult) diagnoseServiceResult {
	if q.Op == "ensure" && r.Err == nil && r.Temporary {
		e.temporary = false
	}
	return r
}
