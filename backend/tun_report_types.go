package main

import (
	"errors"
	"fmt"
	"net/http"
)

type wintunStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Version   string `json:"version,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Signer    string `json:"signer,omitempty"`
	Detail    string `json:"detail,omitempty"`
}
type adapterReport struct {
	Name      string
	Present   bool
	Status    string
	IPv4Route bool
	IPv6Route bool
	RouteVia  []string
}
type tunReportRequest struct {
	Op   string
	Name string
	IPv6 bool
}
type tunReportEffect struct{ Kind, Text string }
type tunReportReply struct {
	Flag         bool
	Number       int
	Text         string
	Names, Other []string
	Err          error
	Wintun       wintunStatus
	Adapter      adapterReport
	Exit         exitIPResult
	Region       regionRecord
	Body         []byte
}
type tunReportResult struct {
	Data map[string]any
	Err  error
}
type tunReportEffects interface {
	Call(tunReportEffect) tunReportReply
	Release()
}
type tunReportOracle struct{ fx tunReportEffects }

func (a *tunReportOracle) call(k, t string) tunReportReply {
	return a.fx.Call(tunReportEffect{Kind: k, Text: t})
}
func (a *tunReportOracle) wintun() wintunStatus {
	target := a.call("wintun-path", "").Text
	s := wintunStatus{Path: target}
	r := a.call("wintun-stat", target)
	if r.Err != nil || r.Flag {
		s.Detail = tunReportNotInstalled
		return s
	}
	s.Installed = true
	s.Version = tunReportWintunVersion
	s.Signer = "WireGuard LLC"
	if h := a.call("wintun-hash", target); h.Err == nil {
		s.SHA256 = h.Text
	}
	return s
}
func (a *tunReportOracle) adapter() (adapterReport, error) {
	if r := a.call("adapter-read", tunReportInterface); r.Err != nil {
		return adapterReport{}, r.Err
	}
	r := a.call("adapter-parse", tunReportInterface)
	if r.Err != nil {
		return adapterReport{}, r.Err
	}
	out := adapterReport{Name: tunReportInterface, Present: r.Flag, Status: r.Text}
	for i, prefix := range r.Names {
		if !a.call("adapter-alias", r.Other[i]).Flag {
			continue
		}
		out.RouteVia = append(out.RouteVia, prefix)
		if prefix == "0.0.0.0/0" {
			out.IPv4Route = true
		}
		if prefix == "::/0" {
			out.IPv6Route = true
		}
	}
	return out, nil
}
func (a *tunReportOracle) tunnelState() (bool, int, string, []string, []string) {
	r := a.call("tunnel-state", "")
	return r.Flag, r.Number, r.Text, r.Names, r.Other
}
func (a *tunReportOracle) lookup(host string) ([]string, error) {
	r := a.call("lookup", host)
	return r.Names, r.Err
}
func (a *tunReportOracle) readProfile() ([]byte, error) {
	r := a.call("read-profile", "")
	return r.Body, r.Err
}
func (a *tunReportOracle) hostname(_ []byte) string { return a.call("hostname", "").Text }
func (a *tunReportOracle) region(node string) (regionRecord, bool) {
	r := a.call("region", node)
	return r.Region, r.Flag
}
func (a *tunReportOracle) unavailable(android bool) error {
	if android {
		authorized, fd, _, _, _ := a.tunnelState()
		if !authorized {
			return errors.New(tunReportNoAuth)
		}
		if fd <= 0 {
			return errors.New(tunReportNoFD)
		}
		return nil
	}
	if !a.call("elevated", "").Flag {
		return errors.New(tunReportNoAdmin)
	}
	if !a.wintun().Installed {
		return errors.New(tunReportNoWintun)
	}
	return nil
}
func runGoTunReport(q tunReportRequest, fx tunReportEffects) tunReportResult {
	a := tunReportOracle{fx: fx}
	var data map[string]any
	switch q.Op {
	case "windows-status":
		data = a.windowsStatus()
	case "windows-checks":
		data = a.windowsChecks()
	case "android-status":
		data = a.androidStatus()
	case "android-checks":
		data = a.androidChecks()
	case "dns":
		data = map[string]any{"check": a.dnsHijack()}
	case "node":
		data = map[string]any{"check": a.nodeDomain()}
	case "path":
		data = map[string]any{"check": a.exitPath(q.Name)}
	case "region":
		data = map[string]any{"check": a.exitRegion()}
	default:
		panic("invalid tun report operation")
	}
	return tunReportResult{Data: data}
}
func runTunReportWithEffects(q tunReportRequest, fx tunReportEffects) (out tunReportResult) {
	defer func() {
		if recover() != nil {
			out = tunReportResult{Err: errors.New("tunnel reporting interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedTunReport(q, fx)
}
func (a *app) runTunReportHTTP(w http.ResponseWriter, q tunReportRequest) {
	runTunReportHTTPWithEffects(w, q, newAppTunReportEffects(a, q))
}
func runTunReportHTTPWithEffects(w http.ResponseWriter, q tunReportRequest, fx tunReportEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedTunReport(q, fx)
	if r.Err != nil {
		http.Error(tracked, r.Err.Error(), 500)
		return
	}
	writeJSON(tracked, r.Data)
}

func tunReportCheck(r tunReportResult) tunCheck {
	if r.Err != nil {
		panic(r.Err)
	}
	v, ok := r.Data["check"].(tunCheck)
	if !ok {
		panic(fmt.Errorf("invalid tun check result"))
	}
	return v
}

const tunReportNoAdmin = "TUN 需要管理员权限：请以管理员身份重启 SmartVPN"

const tunReportNoWintun = "缺少 wintun.dll：请先下载，或在设置中指定它的路径"

const tunReportNoAuth = "VPN 未授权：请在系统的连接请求里允许 SmartVPN"

const tunReportNoFD = "隧道尚未建立：先打开连接开关"

const tunReportInterface = "SmartVPN"
const tunReportWintunVersion = "0.14.1"

const tunReportNotInstalled = "尚未安装"
