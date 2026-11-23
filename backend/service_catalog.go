package main

import (
	"errors"
	"math"
	"reflect"
)

// InfoHandle references the original Go pointer resource. Values are not mirrored.
type serviceCatalogEntry struct {
	ID, URL, Label, Prefix string
	Enabled                bool
	UpdatedAt              int64
	NodeCount              int
	LastError              string
	InfoHandle             uint64
	QuotaUpdatedAt         int64
	QuotaError             string
}
type serviceCatalogState struct {
	Entries   []serviceCatalogEntry
	FromCache bool
	HasBackup bool
	Backup    []serviceCatalogEntry
}
type serviceCatalogCommand struct {
	Action  uint8
	Index   int
	Mask    uint64
	Entry   serviceCatalogEntry
	Entries []serviceCatalogEntry
	ID, URL string
	Flag    bool
}
type serviceCatalogQuery struct {
	Kind    uint8
	Index   int
	ID, URL string
	Handle  uint64
}
type serviceCatalogReply struct {
	Found                            bool
	Count                            int
	NilEntries, FromCache, HasBackup bool
	Entry                            serviceCatalogEntry
	Entries                          []serviceCatalogEntry
	References                       uint64
}

const (
	catalogID uint64 = 1 << iota
	catalogURL
	catalogLabel
	catalogPrefix
	catalogEnabled
	catalogUpdatedAt
	catalogNodeCount
	catalogLastError
	catalogInfoHandle
	catalogQuotaUpdatedAt
	catalogQuotaError
	catalogAll = (1 << 11) - 1
)
const (
	catalogReset uint8 = 1 + iota
	catalogAppend
	catalogTruncate
	catalogPatch
	catalogBeginDelete
	catalogRemoveID
	catalogRestoreDelete
	catalogFinishDelete
	catalogSetFromCache
	catalogQuotaGuarded
)

// Queue exists only for an application-bound invocation, not as live state.
// The executor consumes an item before submitting; uncertain commits cannot replay.
type catalogIntent struct {
	Action  uint8
	Index   int
	Mask    uint64
	Entry   subscriptionEntry
	ID, URL string
	Flag    bool
}
type catalogIntentQueue struct {
	Items   []catalogIntent
	Applied int
}

func markCatalogIntent(s *settingsServiceState, c catalogIntent) {
	if s.CatalogIntents != nil {
		s.CatalogIntents.Items = append(s.CatalogIntents.Items, c)
	}
}

func catalogPatchValue(v serviceCatalogEntry, c serviceCatalogCommand) (serviceCatalogEntry, error) {
	if c.Mask&^uint64(catalogAll) != 0 {
		return v, errors.New("catalog mask")
	}
	if c.Mask&catalogID != 0 {
		v.ID = c.Entry.ID
	}
	if c.Mask&catalogURL != 0 {
		v.URL = c.Entry.URL
	}
	if c.Mask&catalogLabel != 0 {
		v.Label = c.Entry.Label
	}
	if c.Mask&catalogPrefix != 0 {
		v.Prefix = c.Entry.Prefix
	}
	if c.Mask&catalogEnabled != 0 {
		v.Enabled = c.Entry.Enabled
	}
	if c.Mask&catalogUpdatedAt != 0 {
		v.UpdatedAt = c.Entry.UpdatedAt
	}
	if c.Mask&catalogNodeCount != 0 {
		v.NodeCount = c.Entry.NodeCount
	}
	if c.Mask&catalogLastError != 0 {
		v.LastError = c.Entry.LastError
	}
	if c.Mask&catalogInfoHandle != 0 {
		v.InfoHandle = c.Entry.InfoHandle
	}
	if c.Mask&catalogQuotaUpdatedAt != 0 {
		v.QuotaUpdatedAt = c.Entry.QuotaUpdatedAt
	}
	if c.Mask&catalogQuotaError != 0 {
		v.QuotaError = c.Entry.QuotaError
	}
	return v, nil
}
func catalogEntryBytes(v serviceCatalogEntry) int {
	w := serviceCatalogWriter{}
	w.entry(v)
	return len(w.b)
}
func (b *goServiceStateBackend) catalogReindex() {
	b.catalogRefs = map[uint64]uint64{}
	b.catalogIDs = map[string]int{}
	b.catalogURLs = map[string]int{}
	b.catalogBytes = 20
	for i, v := range b.catalog.Entries {
		b.catalogBytes += catalogEntryBytes(v)
		if _, ok := b.catalogIDs[v.ID]; !ok {
			b.catalogIDs[v.ID] = i
		}
		if _, ok := b.catalogURLs[v.URL]; !ok {
			b.catalogURLs[v.URL] = i
		}
		if v.InfoHandle != 0 {
			b.catalogRefs[v.InfoHandle]++
		}
	}
	for _, v := range b.catalog.Backup {
		b.catalogBytes += catalogEntryBytes(v)
		if v.InfoHandle != 0 {
			b.catalogRefs[v.InfoHandle]++
		}
	}
}
func (b *goServiceStateBackend) applyCatalog(c serviceCatalogCommand) error {
	if b.catalogRefs == nil {
		b.catalogReindex()
	}
	s := &b.catalog
	if c.Action == catalogPatch || c.Action == catalogQuotaGuarded {
		if c.Index < 0 || c.Index >= len(s.Entries) {
			return errors.New("catalog index")
		}
		if c.Action == catalogQuotaGuarded {
			first, ok := b.catalogIDs[c.ID]
			if !ok || first != c.Index || s.Entries[first].URL != c.URL || c.Mask & ^uint64(catalogInfoHandle|catalogQuotaUpdatedAt|catalogQuotaError) != 0 {
				return errors.New("quota guard")
			}
		}
		old := s.Entries[c.Index]
		v, e := catalogPatchValue(old, c)
		if e != nil {
			return e
		}
		size := b.catalogBytes - catalogEntryBytes(old) + catalogEntryBytes(v)
		if size > serviceCatalogMaxBytes-1024 {
			return errors.New("catalog budget")
		}
		s.Entries[c.Index] = v
		b.catalogBytes = size
		if old.InfoHandle != 0 {
			b.catalogRefs[old.InfoHandle]--
			if b.catalogRefs[old.InfoHandle] == 0 {
				delete(b.catalogRefs, old.InfoHandle)
			}
		}
		if v.InfoHandle != 0 {
			b.catalogRefs[v.InfoHandle]++
		}
		if old.ID != v.ID || old.URL != v.URL {
			b.catalogReindex()
		}
		return nil
	}
	switch c.Action {
	case catalogSetFromCache:
		s.FromCache = c.Flag
		return nil
	case catalogAppend:
		if len(s.Entries) >= serviceCatalogMaxItems || b.catalogBytes+catalogEntryBytes(c.Entry) > serviceCatalogMaxBytes-1024 {
			return errors.New("catalog append budget")
		}
		i := len(s.Entries)
		s.Entries = append(s.Entries, c.Entry)
		b.catalogBytes += catalogEntryBytes(c.Entry)
		if c.Entry.InfoHandle != 0 {
			b.catalogRefs[c.Entry.InfoHandle]++
		}
		if _, ok := b.catalogIDs[c.Entry.ID]; !ok {
			b.catalogIDs[c.Entry.ID] = i
		}
		if _, ok := b.catalogURLs[c.Entry.URL]; !ok {
			b.catalogURLs[c.Entry.URL] = i
		}
		return nil
	case catalogReset:
		if s.HasBackup {
			return errors.New("catalog active backup")
		}
		s.Entries = nil
	case catalogTruncate:
		if len(s.Entries) == 0 {
			return errors.New("catalog empty truncate")
		}
		s.Entries = s.Entries[:len(s.Entries)-1]
	case catalogBeginDelete:
		if s.HasBackup {
			return errors.New("catalog active backup")
		}
		size := b.catalogBytes
		for _, v := range s.Entries {
			size += catalogEntryBytes(v)
		}
		if size > serviceCatalogMaxBytes-1024 {
			return errors.New("catalog backup budget")
		}
		s.Backup = append([]serviceCatalogEntry(nil), s.Entries...)
		s.HasBackup = true
	case catalogRemoveID:
		kept := make([]serviceCatalogEntry, 0, len(s.Entries))
		for _, v := range s.Entries {
			if v.ID != c.ID {
				kept = append(kept, v)
			}
		}
		s.Entries = kept
	case catalogRestoreDelete:
		if !s.HasBackup {
			return errors.New("missing catalog backup")
		}
		size := 20
		for _, v := range s.Backup {
			size += 2 * catalogEntryBytes(v)
		}
		if size > serviceCatalogMaxBytes-1024 {
			return errors.New("catalog restore budget")
		}
		s.Entries = append([]serviceCatalogEntry(nil), s.Backup...)
	case catalogFinishDelete:
		s.HasBackup = false
		s.Backup = nil
	default:
		return errors.New("catalog action")
	}
	b.catalogReindex()
	return nil
}
func (b *goServiceStateBackend) CatalogQuery(payload string) (string, error) {
	if b.closed {
		return "", errServiceStateClosed
	}
	q, e := decodeServiceCatalogQuery(payload)
	if e != nil {
		return "", e
	}
	if b.catalogRefs == nil {
		b.catalogReindex()
	}
	s := b.catalog
	r := serviceCatalogReply{Count: len(s.Entries), NilEntries: s.Entries == nil, FromCache: s.FromCache, HasBackup: s.HasBackup}
	index := -1
	switch q.Kind {
	case 0:
	case 1:
		index = q.Index
	case 2:
		r.Entries = s.Entries
	case 3:
		if i, ok := b.catalogIDs[q.ID]; ok {
			index = i
		}
	case 4:
		for _, pair := range []struct {
			k string
			m map[string]int
		}{{q.URL, b.catalogURLs}, {q.ID, b.catalogIDs}} {
			if i, ok := pair.m[pair.k]; ok && (index < 0 || i < index) {
				index = i
			}
		}
	case 5:
		r.References = b.catalogRefs[q.Handle]
	default:
		return "", errors.New("catalog query")
	}
	if index >= 0 && index < len(s.Entries) {
		r.Found = true
		r.Entry = s.Entries[index]
		r.Count = index
	}
	return encodeServiceCatalogReply(r)
}
func markCatalogAppend(s *settingsServiceState, e subscriptionEntry) {
	markCatalogIntent(s, catalogIntent{Action: catalogAppend, Entry: e})
}
func markCatalogFromCache(s *settingsServiceState, v bool) {
	markCatalogIntent(s, catalogIntent{Action: catalogSetFromCache, Flag: v})
}
func (a *app) catalogQueryLocked(q serviceCatalogQuery) serviceCatalogReply {
	s := a.serviceStateLocked()
	if s.closed || s.pending != nil || s.catalogFault != nil {
		panic("catalog unavailable")
	}
	p, e := encodeServiceCatalogQuery(q)
	if e != nil {
		panic(e)
	}
	p, e = s.backend.CatalogQuery(p)
	if e != nil {
		panic(e)
	}
	r, e := decodeServiceCatalogReply(p)
	if e != nil {
		panic(e)
	}
	return r
}
func (s *serviceAuthority) registerQuota(p *subscriptionUserInfo) uint64 {
	if s.closed || s.pending != nil || s.catalogFault != nil {
		panic("catalog unavailable")
	}
	if p == nil {
		return 0
	}
	if s.infos == nil {
		s.quotaStore = newQuotaStore()
		if s.quotaStore == nil {
			s.infoReverse = map[*subscriptionUserInfo]uint64{}
		}
		s.infos = map[uint64]*subscriptionUserInfo{}
	}
	if h := s.infoReverse[p]; h != 0 {
		return h
	}
	if s.nextInfo == math.MaxUint64 {
		panic("catalog resource exhausted")
	}
	s.nextInfo++
	if s.pendingInfos == nil {
		s.pendingInfos = map[uint64]bool{}
	}
	s.pendingInfos[s.nextInfo] = true
	if s.quotaStore != nil {
		s.writeQuota(s.nextInfo, p)
		s.infos[s.nextInfo] = nil // membership only; Rust owns the counters
	} else {
		s.infos[s.nextInfo] = p
		s.infoReverse[p] = s.nextInfo
	}
	return s.nextInfo
}
func (s *serviceAuthority) packCatalog(v subscriptionEntry) serviceCatalogEntry {
	return catalogEntryWithQuota(v, s.registerQuota(v.UserInfo))
}
func catalogEntryWithQuota(v subscriptionEntry, h uint64) serviceCatalogEntry {
	return serviceCatalogEntry{v.ID, v.URL, v.Label, v.Prefix, v.Enabled, v.UpdatedAt, v.NodeCount, v.LastError, h, v.QuotaUpdatedAt, v.QuotaError}
}

// Only append and quota-changing patches acquire a value. Metadata updates
// must preserve the existing handle even when the planner carries a snapshot.
func (a *app) commitCatalogIntentLocked(v catalogIntent) {
	var h uint64
	if v.Action == catalogAppend || (v.Action == catalogPatch && v.Mask&catalogInfoHandle != 0) {
		h = a.serviceStateLocked().registerQuota(v.Entry.UserInfo)
	}
	a.catalogCommandLocked(serviceCatalogCommand{Action: v.Action, Index: v.Index, Mask: v.Mask, Entry: catalogEntryWithQuota(v.Entry, h), ID: v.ID, URL: v.URL, Flag: v.Flag})
}

var errMissingQuota = errors.New("catalog resource missing")

func (s *serviceAuthority) unpackCatalog(v serviceCatalogEntry) subscriptionEntry {
	return s.catalogProjection(v, false)
}
func (s *serviceAuthority) catalogProjection(v serviceCatalogEntry, detached bool) subscriptionEntry {
	info := s.quotaSnapshot(v.InfoHandle, detached)
	return subscriptionEntry{v.ID, v.URL, v.Label, v.Prefix, v.Enabled, v.UpdatedAt, v.NodeCount, v.LastError, info, v.QuotaUpdatedAt, v.QuotaError}
}
func (a *app) catalogEntriesLocked() []subscriptionEntry {
	return a.projectCatalogEntriesLocked(false)
}
func (a *app) projectCatalogEntriesLocked(detached bool) []subscriptionEntry {
	v := a.catalogQueryLocked(serviceCatalogQuery{Kind: 2}).Entries
	if v == nil {
		return nil
	}
	out := make([]subscriptionEntry, len(v))
	for i, e := range v {
		out[i] = a.serviceStateLocked().catalogProjection(e, detached)
	}
	return out
}
func (a *app) catalogEntryLocked(i int) (subscriptionEntry, bool) {
	r := a.catalogQueryLocked(serviceCatalogQuery{Kind: 1, Index: i})
	return a.serviceStateLocked().unpackCatalog(r.Entry), r.Found
}
func (a *app) profileFromCacheLocked() bool {
	return a.catalogQueryLocked(serviceCatalogQuery{}).FromCache
}
func (a *app) setProfileFromCacheLocked(v bool) {
	a.catalogCommandLocked(serviceCatalogCommand{Action: catalogSetFromCache, Flag: v})
}
func (a *app) patchCatalogEntryLocked(i int, mask uint64, e subscriptionEntry) {
	a.commitCatalogIntentLocked(catalogIntent{Action: catalogPatch, Index: i, Mask: mask, Entry: e})
}
func (a *app) restoreCatalogEntriesLocked(es []subscriptionEntry) {
	a.catalogCommandLocked(serviceCatalogCommand{Action: catalogReset})
	if es != nil && len(es) == 0 {
		a.catalogCommandLocked(serviceCatalogCommand{Action: catalogAppend})
		a.catalogCommandLocked(serviceCatalogCommand{Action: catalogTruncate})
		return
	}
	for _, e := range es {
		a.catalogCommandLocked(serviceCatalogCommand{Action: catalogAppend, Entry: a.serviceStateLocked().packCatalog(e)})
	}
}
func (a *app) applyCatalogIntentsLocked(s *settingsServiceState) {
	if s.CatalogIntents == nil {
		return
	}
	q := s.CatalogIntents
	for q.Applied < len(q.Items) {
		v := q.Items[q.Applied]
		q.Applied++
		a.commitCatalogIntentLocked(v)
	}
}

func catalogCommandHandles(c serviceCatalogCommand) map[uint64]bool {
	m := map[uint64]bool{}
	if c.Entry.InfoHandle != 0 {
		m[c.Entry.InfoHandle] = true
	}
	for _, v := range c.Entries {
		if v.InfoHandle != 0 {
			m[v.InfoHandle] = true
		}
	}
	return m
}
func (a *app) catalogCommandLocked(c serviceCatalogCommand) {
	s := a.serviceStateLocked()
	committed := false
	defer func() {
		if !committed && s.pending == nil {
			for h := range s.pendingInfos {
				s.retireQuota(h)
			}
			s.pendingInfos = nil
		}
		if v := recover(); v != nil {
			if committed {
				s.catalogFault = errors.New("catalog resource retirement failed")
			}
			panic(v)
		}
	}()
	if s.closed || s.pending != nil || s.catalogFault != nil {
		panic("catalog authority unavailable")
	}
	if s.retireInfos == nil {
		s.retireInfos = map[uint64]bool{}
	}
	candidates := s.retireInfos
	for h := range s.lastInfoHandles {
		candidates[h] = true
	}
	if c.Action == catalogPatch || c.Action == catalogQuotaGuarded {
		r := a.catalogQueryLocked(serviceCatalogQuery{Kind: 1, Index: c.Index})
		if r.Entry.InfoHandle != 0 {
			candidates[r.Entry.InfoHandle] = true
		}
	}
	if c.Action == catalogReset || c.Action == catalogRemoveID || c.Action == catalogRestoreDelete || c.Action == catalogFinishDelete || c.Action == catalogTruncate {
		for h := range s.infos {
			candidates[h] = true
		}
	}
	p, e := encodeServiceCatalogCommand(c)
	if e != nil {
		panic(e)
	}
	if e = s.mutate(serviceStateCommand{Kind: 6, CatalogPayload: p}); e != nil {
		panic(e)
	}
	committed = true
	s.pendingInfos = nil
	s.lastInfoHandles = catalogCommandHandles(c)
	for h := range candidates {
		if s.lastInfoHandles[h] {
			delete(candidates, h)
			continue
		}
		r := a.catalogQueryLocked(serviceCatalogQuery{Kind: 5, Handle: h})
		if r.References == 0 {
			s.retireQuota(h)
		}
		delete(candidates, h)
	}
}
func (s *serviceAuthority) validateCatalogResources(st serviceCatalogState, last serviceStateCommand) error {
	check := func(h uint64) error {
		p, ok := s.infos[h]
		if h != 0 && (!ok || (p == nil && s.quotaStore == nil) || (p != nil && s.infoReverse[p] != h)) {
			return errors.New("catalog checkpoint resource missing")
		}
		return nil
	}
	for _, list := range [][]serviceCatalogEntry{st.Entries, st.Backup} {
		for _, v := range list {
			if e := check(v.InfoHandle); e != nil {
				return e
			}
		}
	}
	if last.Kind == 6 {
		c, e := decodeServiceCatalogCommand(last.CatalogPayload)
		if e != nil {
			return e
		}
		for h := range catalogCommandHandles(c) {
			if e := check(h); e != nil {
				return e
			}
		}
	}
	return nil
}
func validateCatalogLast(st serviceCatalogState, c serviceCatalogCommand) error {
	bad := errors.New("catalog checkpoint command mismatch")
	switch c.Action {
	case catalogReset:
		if st.Entries != nil {
			return bad
		}
	case catalogAppend:
		if len(st.Entries) == 0 || st.Entries[len(st.Entries)-1] != c.Entry {
			return bad
		}
	case catalogTruncate:
		if st.Entries == nil {
			return bad
		}
	case catalogPatch, catalogQuotaGuarded:
		if c.Index < 0 || c.Index >= len(st.Entries) {
			return bad
		}
		v, e := catalogPatchValue(st.Entries[c.Index], c)
		if e != nil || v != st.Entries[c.Index] {
			return bad
		}
		if c.Action == catalogQuotaGuarded {
			if c.Mask&^uint64(catalogInfoHandle|catalogQuotaUpdatedAt|catalogQuotaError) != 0 {
				return bad
			}
			found := false
			for i, v := range st.Entries {
				if v.ID == c.ID {
					found = true
					if i != c.Index || v.URL != c.URL {
						return bad
					}
					break
				}
			}
			if !found {
				return bad
			}
		}
	case catalogBeginDelete:
		if !st.HasBackup || !reflect.DeepEqual(st.Backup, append([]serviceCatalogEntry(nil), st.Entries...)) {
			return bad
		}
	case catalogRemoveID:
		if st.Entries == nil {
			return bad
		}
		for _, v := range st.Entries {
			if v.ID == c.ID {
				return bad
			}
		}
	case catalogRestoreDelete:
		if !st.HasBackup || !reflect.DeepEqual(st.Entries, append([]serviceCatalogEntry(nil), st.Backup...)) {
			return bad
		}
	case catalogFinishDelete:
		if st.HasBackup || st.Backup != nil {
			return bad
		}
	case catalogSetFromCache:
		if st.FromCache != c.Flag {
			return bad
		}
	default:
		return bad
	}
	return nil
}

// Element writes historically affect live shared entries before ordinary IO;
// structural slice-header changes become visible at composite/final commit.
func (a *app) flushCatalogElementIntentsLocked(s *settingsServiceState) {
	q := s.CatalogIntents
	if q == nil {
		return
	}
	for q.Applied < len(q.Items) {
		v := q.Items[q.Applied]
		if v.Action != catalogPatch {
			return
		}
		q.Applied++
		a.commitCatalogIntentLocked(v)
	}
}

func validateServiceCatalogState(s serviceCatalogState) error {
	if !s.HasBackup && s.Backup != nil {
		return errors.New("inactive catalog backup")
	}
	if len(s.Entries) > serviceCatalogMaxItems || len(s.Backup) > serviceCatalogMaxItems {
		return errors.New("catalog count")
	}
	n := 20
	for _, v := range s.Entries {
		n += catalogEntryBytes(v)
	}
	for _, v := range s.Backup {
		n += catalogEntryBytes(v)
	}
	if n > serviceCatalogMaxBytes-1024 {
		return errors.New("catalog state budget")
	}
	return nil
}
