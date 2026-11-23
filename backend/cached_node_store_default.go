//go:build !rustcore || (!windows && !android) || (windows && !amd64) || (android && (!cgo || (!amd64 && !arm64)))

package main

func newNodeCacheStore() nodeCacheStore { return nil }

func newQuotaStore() quotaCommands { return nil }

func newNodeGraphStore() graphCommands { return nil }
