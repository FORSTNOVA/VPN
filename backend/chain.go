package main

import (
	"net/http"
)

const chainExit = "SmartVPNChain"

type proxyChain struct {
	Name    string   `json:"name,omitempty"`
	Enabled bool     `json:"enabled"`
	Hops    []string `json:"hops,omitempty"`
}

// Build private copies. A provider refresh or an ordinary single-node
// connection must never inherit the dialer of a chain member.
func chainProxies(hops []string, profile []byte, manual []manualNode) ([]map[string]any, error) {
	r := (*app)(nil).runChainService(chainServiceRequest{Op: "chain", Hops: hops, Profile: profile, Manual: manual})
	return r.Proxies, r.Err
}

func prefixProxies(hops []string, profile []byte, manual []manualNode) ([]map[string]any, error) {
	r := (*app)(nil).runChainService(chainServiceRequest{Op: "prefix", Hops: hops, Profile: profile, Manual: manual})
	return r.Proxies, r.Err
}

func proxyIndex(profile []byte, manual []manualNode) (map[string][]map[string]any, error) {
	fx := &appMetadataProjectionEffects{body: profile, manual: manual}
	r := runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "index"}, fx)
	return fx.index, r.Err
}
func compilePrefix(hops []string, index map[string][]map[string]any) ([]map[string]any, error) {
	r := (*app)(nil).runChainService(chainServiceRequest{Op: "compile", Hops: hops, Index: index})
	return r.Proxies, r.Err
}

func (a *app) buildChain(next proxyChain, profile []byte) ([]byte, error) {
	r := a.runChainService(chainServiceRequest{Op: "build", Next: next, Profile: profile})
	return r.Body, r.Err
}

func (a *app) updateChain(w http.ResponseWriter, r *http.Request) {
	var next proxyChain
	if err := decodeJSON(r.Body, &next); err != nil || len(next.Hops) > 8 {
		http.Error(w, chainHTTPMessage0(), 400)
		return
	}
	result := a.runChainService(chainServiceRequest{Op: "update", Next: next})
	if result.Err != nil {
		status := result.Status
		if status < 400 {
			status = 500
		}
		http.Error(w, result.Message, status)
		return
	}
	writeJSON(w, result.Next)
}

func (a *app) saveChainProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Original string
		Hops           []string
	}
	if decodeJSON(r.Body, &req) != nil {
		http.Error(w, chainHTTPMessage4(), 400)
		return
	}
	result := a.runChainService(chainServiceRequest{Op: "save-profile", Name: req.Name, Original: req.Original, Hops: req.Hops})
	if result.Err != nil {
		status := result.Status
		if status < 400 {
			status = 500
		}
		http.Error(w, result.Message, status)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
