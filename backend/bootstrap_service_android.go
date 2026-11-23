//go:build android

package main

import (
	"errors"
	"fmt"
)

type bootstrapAndroidResources struct {
	report     tunnelReport
	fd         int
	decode     func(*tunnelReport) error
	bridgeInit func() bool
	socketHook func()
}

func (e *appBootstrapEffects) android() *bootstrapAndroidResources {
	if e.platformData == nil {
		e.platformData = &bootstrapAndroidResources{}
	}
	return e.platformData.(*bootstrapAndroidResources)
}
func (e *appBootstrapEffects) platformCall(q bootstrapServiceEffect) (r bootstrapServiceReply) {
	p := e.android()
	switch q.Kind {
	case "service-lock":
		serviceMu.Lock()
		e.serviceUnlock = serviceMu.Unlock
	case "service-unlock":
		if f := e.serviceUnlock; f != nil && !e.deferServiceUnlock {
			e.serviceUnlock = nil
			f()
		}
	case "android-publish":
		service, serviceToken, serviceAPI = e.a, e.token, e.server
		e.serverClose = nil
	case "android-detach":
		e.a, e.server = service, serviceAPI
		service, serviceToken, serviceAPI = nil, "", nil
		r.Flag = e.a != nil
		r.Other = e.server != nil
		if e.a != nil {
			e.home = e.a.home
		}
		if e.a != nil && e.server != nil {
			e.serverClose = e.server.Close
		}
	case "android-current":
		e.a = service
		r.Flag = e.a != nil
	case "android-reuse":
		e.port, e.token = e.a.apiPort, serviceToken
	case "bridge-init":
		r.Flag = p.bridgeInit()
	case "socket-hook":
		p.socketHook()
	case "tunnel-decode":
		r.Err = p.decode(&p.report)
	case "tunnel-grant":
		tunnel.grant(true, p.fd, p.report.Interface, p.report.Routes, p.report.DNS)
	case "tunnel-release":
		tunnel.release()
	case "failure":
		switch q.Count {
		case 5:
			r.Err = errors.New(bootstrapBridgeInitError)
		case 6:
			r.Err = errors.New(bootstrapTunnelAbsentError)
		default:
			r.Err = fmt.Errorf("unsupported bootstrap failure %d", q.Count)
		}
	default:
		panic("unknown Android bootstrap effect " + q.Kind)
	}
	if r.Err != nil {
		e.lastErr = r.Err
	}
	return
}
