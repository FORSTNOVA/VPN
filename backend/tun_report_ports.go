package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type appTunReportEffects struct {
	lastRegion string
	adapterRaw []byte
	proxy      *url.URL
	a          *app
	q          tunReportRequest
	owned      bool
	profile    []byte
	lookup     func(string) ([]string, error)
}

func newAppTunReportEffects(a *app, q tunReportRequest) *appTunReportEffects {
	return &appTunReportEffects{a: a, q: q, lookup: net.LookupHost}
}
func (e *appTunReportEffects) Release() {
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appTunReportEffects) Call(q tunReportEffect) (r tunReportReply) {
	switch q.Kind {
	case "quote":
		r.Text = fmt.Sprintf("%q", q.Text)
	case "lock":
		if e.owned {
			panic("tun report double lock")
		}
		e.a.mu.Lock()
		e.owned = true
		_ = e.a.controlMetaLocked()
	case "unlock":
		e.Release()
	case "running":
		r.Flag = e.a.kernelRunningLocked()
	case "mode":
		r.Text = e.a.connectionModeLocked()
	case "read-profile":
		e.profile, r.Err = os.ReadFile(e.a.profilePath())
		r.Body = e.profile
	case "hostname":
		r.Text = proxyServerHostname(e.profile)
	case "lookup":
		host := q.Text
		if host == "" {
			host = probeDomain()
		}
		r.Names, r.Err = e.lookup(host)
	case "fake-address":
		r.Flag = isFakeAddress(q.Text)
	case "active-node":
		r.Text = e.a.activeNodeLocked()
	case "region":
		r.Region, r.Flag = e.a.regionRecordLocked(q.Text)
		e.lastRegion = r.Region.Country
	case "locked-region":
		r.Text = e.a.controlMetaLocked().RuntimeLockedRegion
		r.Flag = strings.EqualFold(r.Text, e.lastRegion)
	case "exit-tunnel":
		proxy := e.proxy
		if e.a != nil {
			proxy = &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(e.a.mixedPort))}
		}
		r.Exit = checkExitIP(proxy, exitIPProbeURL(e.q.IPv6), e.q.IPv6)
	default:
		return e.platform(q)
	}
	return
}
