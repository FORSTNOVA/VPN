package main

import (
	"errors"
	"net/http"
)

type driverInstallRequest struct {
	Op, Path string
	Body     []byte
}
type driverInstallEffect struct {
	Kind, Text, Other string
	Number            int64
}
type driverInstallReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type driverInstallResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type driverInstallEffects interface {
	Call(driverInstallEffect) driverInstallReply
	Release()
}
type driverInstallOracle struct{ fx driverInstallEffects }

func (a *driverInstallOracle) call(k, t, o string, n int64) driverInstallReply {
	return a.fx.Call(driverInstallEffect{Kind: k, Text: t, Other: o, Number: n})
}
func runDriverInstallWithEffects(q driverInstallRequest, fx driverInstallEffects) (out driverInstallResult) {
	defer func() {
		if recover() != nil {
			out = driverInstallResult{Status: 500, Err: errors.New("driver operation interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedDriverInstall(q, fx)
}
func runDriverInstallHTTPWithEffects(w http.ResponseWriter, q driverInstallRequest, fx driverInstallEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedDriverInstall(q, fx)
	if r.Err != nil {
		status := r.Status
		if status < 400 {
			status = 500
		}
		http.Error(tracked, r.Err.Error(), status)
		return
	}
	writeJSON(tracked, r.Data)
}

var driverBadWintun = errors.New("wintun.dll 未通过校验")
var driverBadArch = errors.New("wintun.dll 不是 64 位 Windows 动态库")

const driverWintunVersion = "0.14.1"
const driverWintunURL = "https://www.wintun.net/builds/wintun-0.14.1.zip"
const driverWintunZipSHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
const driverWintunEntry = "wintun/bin/amd64/wintun.dll"
const driverWintunDLLSHA256 = "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"
const driverWintunSigner = "WireGuard LLC"
