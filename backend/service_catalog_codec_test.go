package main

import (
	"reflect"
	"testing"
)

func catalogCodecEntry() serviceCatalogEntry {
	return serviceCatalogEntry{ID: string([]byte{255, 0}), URL: "opaque", Label: "label", Prefix: "prefix", Enabled: true, UpdatedAt: -4, NodeCount: 7, LastError: "error", InfoHandle: 1 << 63, QuotaUpdatedAt: -6, QuotaError: "quota"}
}
func TestServiceStateCatalogCodec(t *testing.T) {
	for _, v := range []serviceCatalogState{{}, {Entries: []serviceCatalogEntry{}}, {Entries: []serviceCatalogEntry{catalogCodecEntry(), catalogCodecEntry()}, FromCache: true, HasBackup: true, Backup: []serviceCatalogEntry{catalogCodecEntry()}}} {
		b, e := encodeServiceCatalogState(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceCatalogState(b)
		if e != nil || !reflect.DeepEqual(got, v) {
			t.Fatal("state", e)
		}
		for n := 1; n < len(b); n++ {
			if _, e := decodeServiceCatalogState(b[:n]); e == nil {
				t.Fatal("truncated", n)
			}
		}
		if _, e = decodeServiceCatalogState(b + "x"); e == nil {
			t.Fatal("trailing")
		}
	}
	for act := uint8(1); act <= 10; act++ {
		v := serviceCatalogCommand{Action: act, Index: -1, Mask: catalogAll, Entry: catalogCodecEntry(), Entries: []serviceCatalogEntry{}, ID: "id", URL: "url", Flag: true}
		b, e := encodeServiceCatalogCommand(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceCatalogCommand(b)
		if e != nil || !reflect.DeepEqual(got, v) {
			t.Fatal("command", e)
		}
	}
	for k := uint8(0); k <= 5; k++ {
		v := serviceCatalogQuery{Kind: k, Index: -1, ID: "id", URL: "url", Handle: ^uint64(0)}
		b, e := encodeServiceCatalogQuery(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceCatalogQuery(b)
		if e != nil || got != v {
			t.Fatal("query", e)
		}
	}
	v := serviceCatalogReply{Found: true, Count: 2, FromCache: true, HasBackup: true, Entry: catalogCodecEntry(), Entries: []serviceCatalogEntry{}, References: ^uint64(0)}
	b, e := encodeServiceCatalogReply(v)
	if e != nil {
		t.Fatal(e)
	}
	got, e := decodeServiceCatalogReply(b)
	if e != nil || !reflect.DeepEqual(got, v) {
		t.Fatal("reply", e)
	}
}

func TestServiceStateCatalogResourceMirrorClone(t *testing.T) {
	info := &subscriptionUserInfo{Total: 99}
	s := subscriptionServiceState{Subscriptions: []subscriptionEntry{{ID: "a", UserInfo: info}, {ID: "b", UserInfo: info}}, CatalogIntents: &catalogIntentQueue{Items: []catalogIntent{{Entry: subscriptionEntry{UserInfo: info}}}}}
	s.Settings.GameBypass = []string{"a"}
	v := subscriptionCloneResourceState(s)
	if v.Subscriptions[0].UserInfo != info || v.Subscriptions[1].UserInfo != info || v.CatalogIntents.Items[0].Entry.UserInfo != info {
		t.Fatal("resource identity lost")
	}
	v.Settings.GameBypass[0] = "changed"
	v.Subscriptions[0].ID = "changed"
	v.CatalogIntents.Items[0].ID = "changed"
	if s.Settings.GameBypass[0] != "a" || s.Subscriptions[0].ID != "a" || s.CatalogIntents.Items[0].ID != "" {
		t.Fatal("ordinary state aliases")
	}
	info.Total = 100
	if v.Subscriptions[1].UserInfo.Total != 100 {
		t.Fatal("resource write through")
	}
	isolated := subscriptionCloneState(s)
	isolated.Subscriptions[0].UserInfo.Total = 0
	if info.Total != 100 {
		t.Fatal("legacy clone isolation changed")
	}
}
