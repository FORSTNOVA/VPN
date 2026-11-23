package main

import (
	"crypto/sha256"
	"maps"
	"reflect"
	"slices"
)

// An app lock spans both the native transaction and the Go resources borrowed
// by persistence and Mihomo. No Go pointer is stored in Rust.
type graphCommands interface {
	call(op, index uintptr, data []byte) int
}

const (
	graphReplaceManual  = 1
	graphReadManual     = 2
	graphAppendManual   = 3
	graphTruncateManual = 5
	graphReplaceChains  = 6
	graphReadChains     = 7
	graphSetActive      = 8
	graphRenameProfile  = 9
	graphKeepManual     = 10
	graphManualCount    = 11
	graphChainEnabled   = 12
	graphReplaceFull    = 13
	graphReadFull       = 14
	graphAppendFull     = 15
	graphKeepFull       = 16
	graphRestoreFull    = 17
	graphCommitFull     = 18
	graphSetFull        = 19
	graphBeginChains    = 20
	graphRollbackChains = 21
	graphCommitChains   = 22
)

func graphField(b, field []byte) []byte { return append(registryInt(b, int64(len(field))), field...) }
func readGraphField(r *quotaReader) []byte {
	n := r.integer()
	if r.bad || n < 0 || n > int64(len(r.b)-r.p) {
		panic("invalid node graph field")
	}
	return r.take(int(n))
}
func graphSnapshot(g graphCommands, op uintptr) quotaReader {
	b := make([]byte, g.call(op, 0, nil))
	if g.call(op, 0, b) != len(b) {
		panic("node graph changed under app lock")
	}
	return quotaReader{b: b}
}
func manualRecord(n manualNode) []byte {
	var b []byte
	for _, s := range [...]string{n.Name, n.Type, n.Server} {
		b = registryBytes(b, s)
	}
	b = registryInt(b, int64(n.Port))
	return registryInt(b, n.AddedAt)
}
func supportsFullManual(g graphCommands) bool {
	_, ok := g.(interface{ fullManualRecords() })
	return ok
}
func manualRow(n manualNode, full bool) []byte {
	meta := manualRecord(n)
	if !full {
		return meta
	}
	b := registryBytes(graphField(nil, meta), n.URI)
	proxy, ok := encodeManualValue(nil, n.Proxy, 0)
	if ok {
		b = append(b, proxy...)
	}
	return b
}
func (a *app) publishManual(g graphCommands, ns []manualNode) {
	full := supportsFullManual(g)
	op := uintptr(graphReplaceManual)
	if full {
		op = graphReplaceFull
	}
	b := registryBool(nil, ns != nil)
	var hashes [][32]byte
	if full && ns != nil {
		hashes = make([][32]byte, len(ns))
	}
	for i, n := range ns {
		row := manualRow(n, full)
		if full {
			hashes[i] = sha256.Sum256(row)
		}
		b = graphField(b, row)
	}
	g.call(op, 0, b)
	a.manualRecordHashes = hashes
}
func graphHops(h []string) []byte {
	b := registryBool(nil, h != nil)
	for _, s := range h {
		b = registryBytes(b, s)
	}
	return b
}
func decodeGraphHops(b []byte) []string {
	r := quotaReader{b: b}
	var hops []string
	if r.byte() != 0 {
		hops = []string{}
	}
	for r.p < len(b) {
		hops = append(hops, string(readGraphField(&r)))
	}
	return hops
}
func graphActive(v proxyChain) []byte {
	b := registryBytes(nil, v.Name)
	b = registryBool(b, v.Enabled)
	return append(b, graphHops(v.Hops)...)
}
func chainRecord(s chainGraphSettings) []byte {
	b := graphField(nil, graphActive(s.Chain))
	b = registryBool(b, s.Chains != nil)
	for _, k := range slices.Sorted(maps.Keys(s.Chains)) {
		b = registryBytes(b, k)
		b = graphField(b, graphHops(s.Chains[k]))
	}
	return b
}
func (a *app) graphLocked() graphCommands {
	if a.nodeGraph == nil {
		if g := newNodeGraphStore(); g != nil {
			a.publishManual(g, a.manualNodes)
			record := chainRecord(a.settings)
			g.call(graphReplaceChains, 0, record)
			a.chainMirrorHash = sha256.Sum256(record)
			a.nodeGraph = g
		}
	}
	return a.nodeGraph
}

// Borrowers may still edit a returned Proxy map under the app lock. Compare
// the compatibility rows with their last publication, not with the authority.
func (a *app) syncManualBorrows(g graphCommands) {
	rows := a.manualNodes
	if (rows == nil) != (a.manualRecordHashes == nil) || len(rows) != len(a.manualRecordHashes) {
		a.publishManual(g, rows)
		return
	}
	for i, n := range rows {
		row := manualRow(n, true)
		digest := sha256.Sum256(row)
		if digest != a.manualRecordHashes[i] {
			g.call(graphSetFull, uintptr(i), row)
			a.manualRecordHashes[i] = digest
		}
	}
}

func (a *app) manualSnapshotLocked() []manualNode {
	g := a.graphLocked()
	if g == nil {
		return slices.Clone(a.manualNodes)
	}
	op := uintptr(graphReadManual)
	full := supportsFullManual(g)
	if full {
		a.syncManualBorrows(g)
		op = graphReadFull
	}
	r := graphSnapshot(g, op)
	var ns []manualNode
	if r.byte() != 0 {
		ns = make([]manualNode, 0, len(a.manualNodes))
	}
	same := (ns == nil) == (a.manualNodes == nil)
	for r.p < len(r.b) {
		row := quotaReader{b: readGraphField(&r)}
		var resource []byte
		if full {
			meta := readGraphField(&row)
			resource = row.b[row.p:]
			row = quotaReader{b: meta}
		}
		n := manualNode{Name: row.str(), Type: row.str(), Server: row.str(), Port: int(row.integer()), AddedAt: row.integer()}
		if row.bad || row.p != len(row.b) {
			panic("invalid manual node record")
		}
		i := len(ns)
		if full {
			rs := quotaReader{b: resource}
			n.URI = string(readGraphField(&rs))
			proxy := rs.b[rs.p:]
			if len(proxy) == 0 {
				// Runtime-only values outside the wire format keep their Go adapter.
				n.Proxy = a.manualNodes[i].Proxy
			} else {
				var ok bool
				n.Proxy, ok = decodeNodeObject(proxy, true)
				if !ok {
					panic("invalid manual proxy record")
				}
			}
		} else if i < len(a.manualNodes) {
			live := a.manualNodes[i]
			n.Proxy, n.URI = live.Proxy, live.URI
			same = same && n.Name == live.Name && n.Type == live.Type && n.Server == live.Server && n.Port == live.Port && n.AddedAt == live.AddedAt
		}
		ns = append(ns, n)
	}
	if !full && (!same || len(ns) != len(a.manualNodes)) {
		// Existing borrowed slices can write through while holding the app lock.
		a.publishManual(g, a.manualNodes)
		return slices.Clone(a.manualNodes)
	}
	return ns
}
func (a *app) replaceManualLocked(ns []manualNode) {
	if g := a.graphLocked(); g != nil {
		a.publishManual(g, ns)
	}
	a.manualNodes = ns
}
func (a *app) appendManualLocked(n manualNode) {
	if g := a.graphLocked(); g != nil {
		if supportsFullManual(g) {
			row := manualRow(n, true)
			g.call(graphAppendFull, 0, row)
			a.manualRecordHashes = append(a.manualRecordHashes, sha256.Sum256(row))
		} else {
			g.call(graphAppendManual, 0, manualRecord(n))
		}
	}
	a.manualNodes = append(a.manualNodes, n)
}
func (a *app) truncateManualLocked(n int) {
	shortened := a.manualNodes[:n]
	if g := a.graphLocked(); g != nil {
		g.call(graphTruncateManual, uintptr(n), nil)
		if supportsFullManual(g) {
			a.manualRecordHashes = a.manualRecordHashes[:n]
		}
	}
	a.manualNodes = shortened
}
func (a *app) chainSnapshotLocked() chainGraphSettings {
	g := a.graphLocked()
	if g == nil {
		s := cloneChainServiceState(&chainServiceState{Chain: a.settings.Chain, Chains: a.settings.Chains})
		return chainGraphSettings{Chain: s.Chain, Chains: s.Chains}
	}
	if supportsChainTransactions(g) {
		record := chainRecord(a.settings)
		if hash := sha256.Sum256(record); hash != a.chainMirrorHash {
			g.call(graphReplaceChains, 0, record)
			a.chainMirrorHash = hash
		}
	}
	r := graphSnapshot(g, graphReadChains)
	active := quotaReader{b: readGraphField(&r)}
	s := chainGraphSettings{}
	s.Chain.Name = active.str()
	s.Chain.Enabled = active.byte() != 0
	s.Chain.Hops = decodeGraphHops(active.b[active.p:])
	if r.byte() != 0 {
		s.Chains = make(map[string][]string)
	}
	for r.p < len(r.b) {
		k := string(readGraphField(&r))
		s.Chains[k] = decodeGraphHops(readGraphField(&r))
	}
	if !reflect.DeepEqual(s, a.settings) {
		if supportsChainTransactions(g) {
			mirror := cloneChainServiceState(&chainServiceState{Chain: s.Chain, Chains: s.Chains})
			a.rememberChains(chainGraphSettings{mirror.Chain, mirror.Chains})
			return s
		}
		a.publishChainsLocked()
		fresh := cloneChainServiceState(&chainServiceState{Chain: a.settings.Chain, Chains: a.settings.Chains})
		return chainGraphSettings{Chain: fresh.Chain, Chains: fresh.Chains}
	}
	return s
}

func supportsChainTransactions(g graphCommands) bool {
	_, ok := g.(interface{ chainTransactions() })
	return ok
}

func (a *app) rememberChains(s chainGraphSettings) {
	a.settings = s
	if supportsChainTransactions(a.nodeGraph) {
		a.chainMirrorHash = sha256.Sum256(chainRecord(s))
	}
}

func (a *app) writeChainsLocked(op uintptr, data []byte, s chainGraphSettings) {
	if g := a.graphLocked(); g != nil {
		g.call(op, 0, data)
	}
	a.rememberChains(s)
}

func (a *app) borrowChainsLocked() chainGraphSettings {
	if supportsChainTransactions(a.graphLocked()) {
		a.chainSnapshotLocked()
	}
	return a.settings
}

func (a *app) replaceChainsLocked(s chainGraphSettings) {
	a.writeChainsLocked(graphReplaceChains, chainRecord(s), s)
}
func (a *app) publishChainsLocked() { a.replaceChainsLocked(a.settings) }
func (a *app) setActiveChainLocked(c proxyChain) {
	s := a.borrowChainsLocked()
	s.Chain = c
	a.writeChainsLocked(graphSetActive, graphActive(c), s)
}
func (a *app) setChainProfileLocked(oldProfiles map[string][]string, original, name string, hops []string) {
	s := a.borrowChainsLocked()
	profiles := maps.Clone(oldProfiles)
	if profiles == nil {
		profiles = make(map[string][]string)
	}
	delete(profiles, original)
	profiles[name] = hops
	s.Chains = profiles
	b := registryBytes(nil, original)
	b = registryBytes(b, name)
	a.writeChainsLocked(graphRenameProfile, append(b, graphHops(hops)...), s)
}

func (a *app) manualCountLocked() int {
	if g := a.graphLocked(); g != nil {
		if supportsFullManual(g) {
			if len(a.manualRecordHashes) != len(a.manualNodes) {
				a.publishManual(g, a.manualNodes)
			}
			return g.call(graphManualCount, 0, nil)
		}
		n := g.call(graphManualCount, 0, nil)
		if n == len(a.manualNodes) {
			return n
		}
		a.publishManual(g, a.manualNodes)
	}
	return len(a.manualNodes)
}
func (a *app) chainEnabledLocked() bool {
	if g := a.graphLocked(); g != nil {
		if supportsChainTransactions(g) {
			a.borrowChainsLocked()
			return g.call(graphChainEnabled, 0, nil) != 0
		}
		enabled := g.call(graphChainEnabled, 0, nil) != 0
		if enabled == a.settings.Chain.Enabled {
			return enabled
		}
		g.call(graphSetActive, 0, graphActive(a.settings.Chain))
	}
	return a.settings.Chain.Enabled
}
