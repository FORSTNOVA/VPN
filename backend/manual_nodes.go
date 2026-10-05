package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	a.manualNodes = nil
	if a.store == nil {
		return
	}
	body, found, err := a.store.getValue(manualNodesKey)
	if err != nil || !found {
		return
	}
	var stored manualNodeList
	if json.Unmarshal([]byte(body), &stored) != nil {
		log.Printf("stored manual nodes were unreadable and are ignored")
		return
	}
	for _, node := range stored.Nodes {
		if node.Name == "" || node.URI == "" || len(node.Proxy) == 0 {
			continue
		}
		a.manualNodes = append(a.manualNodes, node)
	}
}

func (a *app) saveManualNodesLocked() error {
	if a.store == nil {
		return nil
	}
	body, err := json.Marshal(manualNodeList{Nodes: a.manualNodes})
	if err != nil {
		return err
	}
	return a.store.setValue(manualNodesKey, string(body))
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
	path := a.manualPath()
	if len(a.manualNodes) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	body, err := manualProviderBody(a.manualNodes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, body, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// manualNodeDetails describes the stored nodes in the shape the node list uses,
// so they can be shown before the kernel has ever run.
func manualNodeDetails(nodes []manualNode) map[string]proxyNode {
	details := make(map[string]proxyNode, len(nodes))
	for _, node := range nodes {
		network, _ := node.Proxy["network"].(string)
		tls, _ := node.Proxy["tls"].(bool)
		wsHost, wsPath := false, false
		if options, ok := node.Proxy["ws-opts"].(map[string]any); ok {
			if path, ok := options["path"].(string); ok && path != "" {
				wsPath = true
			}
			if _, ok := options["headers"]; ok {
				wsHost = true
			}
		}
		details[node.Name] = proxyNode{
			Name: node.Name, Type: node.Type, Network: network,
			TLS: tls, WsHost: wsHost, WsPath: wsPath, Manual: true,
		}
	}
	return details
}

// addManualNodeLocked parses a pasted link and stores it. The caller holds a.mu.
func (a *app) addManualNodeLocked(uri, name string) (manualNode, error) {
	node, err := parseNodeURI(uri)
	if err != nil {
		return manualNode{}, err
	}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		node.Name = trimmed
	}
	for _, existing := range a.manualNodes {
		if existing.Name == node.Name {
			return manualNode{}, fmt.Errorf("已经有一个名为「%s」的手动节点", node.Name)
		}
	}
	for _, cached := range a.cachedNodes {
		if cached.Name == node.Name {
			return manualNode{}, fmt.Errorf("订阅里已经有一个名为「%s」的节点，请换个名字", node.Name)
		}
	}
	node.AddedAt = time.Now().Unix()
	a.manualNodes = append(a.manualNodes, node)
	if err := a.saveManualNodesLocked(); err != nil {
		a.manualNodes = a.manualNodes[:len(a.manualNodes)-1]
		return manualNode{}, errors.New("无法保存手动节点")
	}
	if err := a.writeManualProviderLocked(); err != nil {
		return manualNode{}, errors.New("无法写入手动节点文件")
	}
	return node, nil
}

// removeManualNodeLocked forgets one node by name. The caller holds a.mu.
func (a *app) removeManualNodeLocked(name string) error {
	kept := make([]manualNode, 0, len(a.manualNodes))
	found := false
	for _, node := range a.manualNodes {
		if node.Name == name {
			found = true
			continue
		}
		kept = append(kept, node)
	}
	if !found {
		return errors.New("没有找到这个手动节点")
	}
	previous := a.manualNodes
	a.manualNodes = kept
	if err := a.saveManualNodesLocked(); err != nil {
		a.manualNodes = previous
		return errors.New("无法保存手动节点列表")
	}
	if err := a.writeManualProviderLocked(); err != nil {
		return errors.New("无法更新手动节点文件")
	}
	return nil
}

// manualServerDomains lists the fake-ip filter entries for the pasted nodes'
// servers, so a manual node's hostname is never answered from that pool either.
func manualServerDomains(nodes []manualNode) []string {
	entries := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if entry, ok := hostnameSuffix(node.Server); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// ---------------------------------------------------------------------------
// HTTP

func (a *app) listManualNodes(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	nodes := make([]map[string]any, 0, len(a.manualNodes))
	for _, node := range a.manualNodes {
		nodes = append(nodes, map[string]any{
			"name": node.Name, "type": node.Type, "server": node.Server,
			"port": node.Port, "addedAt": node.AddedAt,
		})
	}
	writeJSON(w, map[string]any{"nodes": nodes, "running": a.kernelRunningLocked()})
}

func (a *app) addManualNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URI  string `json:"uri"`
		Name string `json:"name"`
		// A preview is the same parse without storing anything, so the UI can
		// show what a pasted link turned into before it is kept.
		Preview bool `json:"preview"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Preview {
		node, err := parseNodeURI(body.URI)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if trimmed := strings.TrimSpace(body.Name); trimmed != "" {
			node.Name = trimmed
		}
		writeJSON(w, map[string]any{"ok": true, "node": describedManualNode(node)})
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		http.Error(w, "断开连接后再增删手动节点", http.StatusConflict)
		return
	}
	node, err := a.addManualNodeLocked(body.URI, body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "node": describedManualNode(node)})
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
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "missing node name", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		http.Error(w, "断开连接后再增删手动节点", http.StatusConflict)
		return
	}
	if err := a.removeManualNodeLocked(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
