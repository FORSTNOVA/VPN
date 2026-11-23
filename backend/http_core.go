package main

import "net/http"

func httpAuthorizedGo(header, token string) bool { return header == "Bearer "+token }
func writeIdentityJSONGo(w http.ResponseWriter, connected bool) {
	writeJSON(w, map[string]any{"app": appName, "connected": connected})
}
