package main

import (
	"reflect"
	"strings"
	"testing"
)

func controlCodecFixture() serviceControlState {
	return serviceControlState{ImportPolicy: true, GameBypass: []string{"", string([]byte{255, 0}), "tcp"}, DomesticWeb: true, WebRTCProtection: true, DNSProtection: true, SubscriptionURL: "opaque-url", MihomoPath: "path", SelectedNode: "selected", SelectionMode: "manual", LockedRegion: "US", LockedNode: "saved", ConnectionMode: "tun", StrictPrivacy: true, WintunPath: "driver", AutoMergeHours: -7, SocksPort: 12345, RuntimeLockedRegion: "JP", RuntimeLockedNode: "runtime"}
}
func TestServiceStateControlCodec(t *testing.T) {
	for _, v := range []serviceControlState{{}, {GameBypass: []string{}}, controlCodecFixture()} {
		b, e := encodeServiceControlState(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceControlState(b)
		if e != nil || !reflect.DeepEqual(got, v) {
			t.Fatal("roundtrip", e)
		}
		for n := 1; n < len(b); n++ {
			if _, e := decodeServiceControlState(b[:n]); e == nil {
				t.Fatal("truncation", n)
			}
		}
		if _, e = decodeServiceControlState(b + "x"); e == nil {
			t.Fatal("trailing")
		}
	}
	for _, mask := range []uint64{0, 1, controlGameBypass, controlAll} {
		v := serviceControlCommand{Action: 2, Mask: mask, Value: controlCodecFixture()}
		b, e := encodeServiceControlCommand(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceControlCommand(b)
		if e != nil || !reflect.DeepEqual(got, v) {
			t.Fatal("command", e)
		}
	}
	for _, v := range []serviceControlCommand{{Action: 0}, {Action: 1, Mask: 1}, {Action: 2, Mask: 1 << 18}} {
		if _, e := encodeServiceControlCommand(v); e == nil {
			t.Fatal("mask")
		}
	}
	if _, e := encodeServiceControlState(serviceControlState{SubscriptionURL: strings.Repeat("x", serviceControlMaxBytes)}); e == nil {
		t.Fatal("limit")
	}
}
