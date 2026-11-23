package main

import (
	"log"
	"net/http"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// The nodes the user pasted live in their own provider file, which the kernel
// treats exactly like the subscription one: they are measured, health checked,
// region verified and failed over with no special casing.
const (
	manualProviderName = "SmartVPNManual"
	manualProviderPath = "providers/manual.yaml"
	manualNodesKey     = "manualNodes"
)

type manualNodeList struct {
	Nodes []manualNode `json:"nodes"`
}

func (a *app) manualPath() string {
	return filepath.Join(a.home, "providers", "manual.yaml")
}

// loadManualNodesLocked reads the stored nodes. Anything unreadable is dropped
// rather than carried into a configuration the kernel would refuse to start.
func (a *app) loadManualNodesLocked() {
	r := a.runManualService(manualServiceRequest{Op: "load", Borrowed: true})
	if r.Err != nil {
		log.Print("manual node load did not complete")
	}
}

func (a *app) saveManualNodesLocked() error {
	return a.runManualService(manualServiceRequest{Op: "save", Borrowed: true}).Err
}

// manualProviderBody renders the proxies the kernel reads.
func manualProviderBody(nodes []manualNode) ([]byte, error) {
	proxies := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		proxies = append(proxies, node.Proxy)
	}
	return yaml.Marshal(map[string]any{"proxies": proxies})
}

// writeManualProviderLocked keeps the provider file in step with the stored
// nodes. With none stored the file is removed, so the kernel is never handed an
// empty provider.
func (a *app) writeManualProviderLocked() error {
	return a.runManualService(manualServiceRequest{Op: "provider", Borrowed: true}).Err
}

// manualNodeDetails describes the stored nodes in the shape the node list uses,
// so they can be shown before the kernel has ever run.
func manualNodeDetails(nodes []manualNode) map[string]proxyNode {
	if details, ok := nativeManualDetails(nodes); ok {
		return details
	}
	fx := &appMetadataProjectionEffects{manual: nodes}
	if r := runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "details"}, fx); r.Err != nil {
		panic(r.Err)
	}
	return fx.details
}

// addManualNodeLocked parses a pasted link and stores it. The caller holds a.mu.
func (a *app) addManualNodeLocked(uri, name string) (manualNode, error) {
	r := a.runManualService(manualServiceRequest{Op: "add", URI: uri, Name: name, Borrowed: true})
	return r.Node, r.Err
}

// removeManualNodeLocked forgets one node by name. The caller holds a.mu.
func (a *app) removeManualNodeLocked(name string) error {
	return a.runManualService(manualServiceRequest{Op: "delete", Name: name, Borrowed: true}).Err
}

// manualServerDomains lists the fake-ip filter entries for the pasted nodes'
// servers, so a manual node's hostname is never answered from that pool either.
func manualServerDomains(nodes []manualNode) []string {
	fx := &appMetadataProjectionEffects{manual: nodes}
	if r := runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "domains"}, fx); r.Err != nil {
		panic(r.Err)
	}
	return fx.domains
}

// ---------------------------------------------------------------------------
// HTTP

func (a *app) listManualNodes(w http.ResponseWriter, _ *http.Request) {
	r := a.runManualService(manualServiceRequest{Op: "list"})
	if r.Err != nil {
		http.Error(w, "manual node list did not complete", 500)
		return
	}
	nodes := make([]map[string]any, 0, len(r.Nodes))
	for _, n := range r.Nodes {
		nodes = append(nodes, describedManualNode(n))
	}
	writeJSON(w, map[string]any{"nodes": nodes, "running": r.Running})
}

func (a *app) addManualNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URI     string `json:"uri"`
		Name    string `json:"name"`
		Preview bool   `json:"preview"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	op := "add"
	if body.Preview {
		op = "preview"
	}
	result := a.runManualService(manualServiceRequest{Op: op, URI: body.URI, Name: body.Name})
	if result.Err != nil || result.Status != 200 {
		status := result.Status
		if status < 400 {
			status = 500
		}
		http.Error(w, result.Message, status)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "node": describedManualNode(result.Node)})
}

// describedManualNode is what the UI needs to show a node it has never seen in
// the kernel: what it is called, what it speaks and where it points.
func describedManualNode(node manualNode) map[string]any {
	return map[string]any{
		"name": node.Name, "type": node.Type, "server": node.Server,
		"port": node.Port, "addedAt": node.AddedAt,
	}
}

func (a *app) deleteManualNode(w http.ResponseWriter, r *http.Request) {
	result := a.runManualService(manualServiceRequest{Op: "delete", Name: r.URL.Query().Get("name")})
	if result.Err != nil || result.Status != 200 {
		status := result.Status
		if status < 400 {
			status = 500
		}
		http.Error(w, result.Message, status)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
