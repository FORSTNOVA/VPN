package main

import (
	"errors"
	"strconv"
)

func runGoMetadataProjection(q metadataProjectionRequest, fx metadataProjectionEffects) metadataProjectionResult {
	call := func(k string, n int64) metadataProjectionReply {
		return fx.Call(metadataProjectionEffect{Kind: k, Number: n})
	}
	switch q.Op {
	case "details":
		n := call("manual-count", 0).Number
		call("details-new", n)
		for i := int64(0); i < n; i++ {
			r := call("detail-facts", i)
			bits := 0
			if r.Flag {
				bits |= 1
			}
			if r.Number&1 != 0 {
				if r.Number&2 != 0 {
					bits |= 2
				}
				if r.Names[3] != "" {
					bits |= 4
				}
			}
			fx.Call(metadataProjectionEffect{Kind: "details-put", Text: r.Names[0], Other: strconv.Itoa(bits), Number: i})
		}
	case "domains":
		n := call("manual-count", 0).Number
		call("domains-new", n)
		for i := int64(0); i < n; i++ {
			r := call("suffix-child", i)
			if r.Flag {
				fx.Call(metadataProjectionEffect{Kind: "domain-append", Text: r.Text, Number: i})
			}
		}
	case "index", "current-index":
		if q.Op == "current-index" {
			if call("subscription", 0).Text != "" {
				call("read-profile", 0)
			}
			call("manual-snapshot", 0)
		}
		r := call("decode-profile", 0)
		if r.Err != nil {
			return metadataProjectionResult{Err: errors.New(metadataProjectionIndexError)}
		}
		profiles := r.Number
		manual := call("manual-count", 0).Number
		call("index-new", 0)
		for i := int64(0); i < profiles; i++ {
			name := call("profile-name", i).Text
			fx.Call(metadataProjectionEffect{Kind: "index-append", Text: name, Number: i})
		}
		for i := int64(0); i < manual; i++ {
			name := call("manual-name", i).Text
			fx.Call(metadataProjectionEffect{Kind: "index-append", Text: name, Number: i})
		}
	case "mode":
		mode := call("mode-read", 0).Text
		platform := call("platform", 0).Text
		if platform == "android" {
			if mode == "vpn" {
				return metadataProjectionResult{Text: mode}
			}
			return metadataProjectionResult{Text: "vpn"}
		}
		if mode == "system-proxy" || mode == "tun" {
			return metadataProjectionResult{Text: mode}
		}
		return metadataProjectionResult{Text: "system-proxy"}
	default:
		panic("unknown metadata projection")
	}
	return metadataProjectionResult{}
}
