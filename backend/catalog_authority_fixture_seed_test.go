package main

import "reflect"

// Fallback rows preserve pointer identity. Rust rows preserve the registered
// value and return a fresh copy even after the caller edits its input.
func catalogQuotaMatches(s *serviceAuthority, got, input *subscriptionUserInfo, registered subscriptionUserInfo) bool {
	if s.quotaStore == nil || input == nil {
		return got == input
	}
	return got != nil && got != input && *got == registered
}
func testCatalogRead(a *app) []subscriptionEntry     { return a.catalogEntriesLocked() }
func testCatalogWrite(a *app, v []subscriptionEntry) { a.restoreCatalogEntriesLocked(v) }
func testCatalogCacheRead(a *app) bool               { return a.profileFromCacheLocked() }
func testCatalogCacheWrite(a *app, v bool)           { a.setProfileFromCacheLocked(v) }

// Fixture edits are explicit authority mutations, not a live value mirror.
// Same-sized edits use narrow patches; resize/ABA fixtures explicitly restore.
func testCatalogEdit(a *app, edit func(*[]subscriptionEntry)) {
	before := a.catalogEntriesLocked()
	next := append([]subscriptionEntry(nil), before...)
	if before != nil && len(before) == 0 {
		next = []subscriptionEntry{}
	}
	if a.service.quotaStore != nil {
		for i := range next {
			if p := next[i].UserInfo; p != nil {
				v := *p
				next[i].UserInfo = &v
			}
		}
	}
	edit(&next)
	if len(before) != len(next) || (before == nil) != (next == nil) {
		a.restoreCatalogEntriesLocked(next)
		return
	}
	for i := range next {
		if !reflect.DeepEqual(before[i], next[i]) || (a.service.quotaStore == nil && before[i].UserInfo != next[i].UserInfo) {
			a.patchCatalogEntryLocked(i, catalogAll, next[i])
		}
	}
}
