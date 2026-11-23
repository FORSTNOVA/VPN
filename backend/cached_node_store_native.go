//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"encoding/binary"
	"runtime"
	"sync"
	"unsafe"
)

var nodeCacheProc = sync.OnceValue(func() uintptr {
	if !rustCoreAvailable() {
		return 0
	}
	proc, _ := nativeLookup(rustCore.handle, "smartvpn_cached_nodes_v1")
	return proc
})

type nativeStoreHandle struct{ proc, handle uintptr }
type rustNodeCache struct{ *nativeStoreHandle }

func newNodeCacheStore() nodeCacheStore {
	proc := nodeCacheProc()
	if proc == 0 {
		return nil
	}
	// This ABI reports failure in the return value. Windows LastError can
	// contain a value left by an allocator after a successful call.
	return &rustNodeCache{openStoreHandle(proc)}
}

func openStoreHandle(proc uintptr) *nativeStoreHandle {
	h, _, _ := nativeCall(proc, 0, 0, 0, 0, 0)
	if h == 0 || h == ^uintptr(0) {
		panic("native store allocation failed")
	}
	c := &nativeStoreHandle{proc, h}
	runtime.SetFinalizer(c, (*nativeStoreHandle).close)
	return c
}

func (c *nativeStoreHandle) release() {
	runtime.SetFinalizer(c, nil)
	c.close()
}

func (c *nativeStoreHandle) close() { nativeCall(c.proc, 4, c.handle, 0, 0, 0) }

func (c *nativeStoreHandle) call(op, index uintptr, data []byte) int {
	n, _, _ := nativeCall(c.proc, op, c.handle, index, uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(c)
	if int(n) < 0 {
		panic("invalid native store operation")
	}
	return int(n)
}

func (c *rustNodeCache) replace(nodes []proxyNode) {
	b := []byte{0}
	if nodes != nil {
		b[0] = 1
	}
	for _, n := range nodes {
		for _, s := range [...]string{n.SourceLabel, n.Name, n.Type, n.Network} {
			b = binary.LittleEndian.AppendUint64(b, uint64(len(s)))
			b = append(b, s...)
		}
		var flags byte
		for i, v := range [...]bool{n.TLS, n.WsHost, n.WsPath, n.Alive, n.Selected, n.Manual} {
			if v {
				flags |= 1 << i
			}
		}
		b = append(b, flags)
	}
	c.call(1, 0, b)
}

func (c *rustNodeCache) snapshot() []proxyNode {
	b := make([]byte, c.call(2, 0, nil))
	if c.call(2, 0, b) != len(b) {
		panic("cached node snapshot changed under the app lock")
	}
	if b[0] == 0 {
		return nil
	}
	nodes := []proxyNode{}
	for b = b[1:]; len(b) > 0; {
		var n proxyNode
		for _, s := range [...]*string{&n.SourceLabel, &n.Name, &n.Type, &n.Network} {
			size := binary.LittleEndian.Uint64(b)
			b = b[8:]
			*s = string(b[:size])
			b = b[size:]
		}
		flags := b[0]
		n.TLS, n.WsHost, n.WsPath = flags&1 != 0, flags&2 != 0, flags&4 != 0
		n.Alive, n.Selected, n.Manual = flags&8 != 0, flags&16 != 0, flags&32 != 0
		nodes = append(nodes, n)
		b = b[1:]
	}
	return nodes
}

func (c *rustNodeCache) label(index int, label string) { c.call(3, uintptr(index), []byte(label)) }

var quotaProc = sync.OnceValue(func() uintptr {
	if !rustCoreAvailable() {
		return 0
	}
	proc, _ := nativeLookup(rustCore.handle, "smartvpn_catalog_quota_v1")
	return proc
})

func newQuotaStore() quotaCommands {
	if proc := quotaProc(); proc != 0 {
		return openStoreHandle(proc)
	}
	return nil
}

var nodeGraphProc = sync.OnceValue(func() uintptr {
	if !rustCoreAvailable() {
		return 0
	}
	proc, _ := nativeLookup(rustCore.handle, "smartvpn_node_graph_v1")
	return proc
})

type fullManualGraph struct{ *nativeStoreHandle }

func (*fullManualGraph) fullManualRecords() {}

var hasFullManualGraph = sync.OnceValue(func() bool {
	proc, _ := nativeLookup(rustCore.handle, "smartvpn_manual_record_version_v1")
	if proc == 0 {
		return false
	}
	v, _, _ := nativeCall(proc)
	return v == 1
})

type chainTransactionGraph struct{ *fullManualGraph }

func (*chainTransactionGraph) chainTransactions() {}

var hasChainTransactions = sync.OnceValue(func() bool {
	proc, _ := nativeLookup(rustCore.handle, "smartvpn_chain_transaction_version_v1")
	if proc == 0 {
		return false
	}
	v, _, _ := nativeCall(proc)
	return v == 1
})

func newNodeGraphStore() graphCommands {
	if proc := nodeGraphProc(); proc != 0 {
		if hasFullManualGraph() {
			g := &fullManualGraph{openStoreHandle(proc)}
			if hasChainTransactions() {
				return &chainTransactionGraph{g}
			}
			return g
		}
		return openStoreHandle(proc)
	}
	return nil
}
