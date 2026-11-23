//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func (e *appTunReportEffects) platform(q tunReportEffect) (r tunReportReply) {
	switch q.Kind {
	case "elevated":
		r.Flag = e.a.elevated
	case "wintun-path":
		r.Text = e.a.wintunInstallPath()
	case "wintun-stat":
		var info os.FileInfo
		info, r.Err = os.Stat(q.Text)
		if r.Err == nil {
			r.Flag = info.IsDir()
		}
	case "wintun-hash":
		r.Text, r.Err = fileSHA256(q.Text)
	case "adapter-read":
		e.adapterRaw, r.Err = readTunAdapterRaw(q.Text)
	case "adapter-parse":
		var v struct {
			AdapterPresent bool
			AdapterStatus  string
			Routes         []struct{ Prefix, Alias, NextHop string }
		}
		if err := json.Unmarshal(bytes.TrimSpace(e.adapterRaw), &v); err != nil {
			r.Err = fmt.Errorf(tunReportAdapterParseError, err)
			return
		}
		r.Flag = v.AdapterPresent
		r.Text = v.AdapterStatus
		for _, route := range v.Routes {
			r.Names = append(r.Names, route.Prefix)
			r.Other = append(r.Other, route.Alias)
		}
	case "adapter-alias":
		r.Flag = strings.EqualFold(q.Text, tunReportInterface)
	case "exit-system":
		r.Exit = noProxyExitIP(exitIPProbeURL(e.q.IPv6), e.q.IPv6)
	default:
		panic("unsupported windows tun effect: " + q.Kind)
	}
	return
}

const tunReportAdapterParseError = "网卡状态无法解析: %w"
