package main

import "slices"

// The app mutex orders a cache replacement with the surrounding subscription
// and selection changes. Rust owns the query snapshot. Settings intents can
// lend a Go backing array to the app and require subsequent element writes
// through that array to remain visible.
type nodeCacheStore interface {
	replace([]proxyNode)
	snapshot() []proxyNode
	label(int, string)
}

func (a *app) replaceCachedNodesLocked(nodes []proxyNode, borrowed bool) {
	if a.nodeCache == nil && nodes != nil {
		a.nodeCache = newNodeCacheStore()
	}
	if a.nodeCache != nil {
		a.nodeCache.replace(nodes)
	}
	a.cachedNodes = nodes
	a.cacheBorrowed = borrowed
}

func (a *app) borrowCachedNodesLocked() []proxyNode {
	a.cacheBorrowed = true
	return a.cachedNodes
}

func (a *app) cachedNodeSnapshotLocked() []proxyNode {
	if a.nodeCache == nil {
		return cloneConnectionNodes(a.cachedNodes)
	}
	nodes := a.nodeCache.snapshot()
	if a.cacheBorrowed && ((nodes == nil) != (a.cachedNodes == nil) || !slices.Equal(nodes, a.cachedNodes)) {
		a.nodeCache.replace(a.cachedNodes)
		return cloneConnectionNodes(a.cachedNodes)
	}
	return nodes
}

func (a *app) setCachedNodeLabelLocked(index int, label string) {
	if a.nodeCache != nil {
		a.nodeCache.label(index, label)
	}
	a.cachedNodes[index].SourceLabel = label
}
