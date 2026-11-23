//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import "net/http"

func httpAuthorized(header, token string) bool                { return httpAuthorizedGo(header, token) }
func writeIdentityJSON(w http.ResponseWriter, connected bool) { writeIdentityJSONGo(w, connected) }

func chooseReachableNode(nodes []proxyNode, delays map[string]int, preferred string) (string, int) {
	return chooseReachableNodeGo(nodes, delays, preferred)
}
