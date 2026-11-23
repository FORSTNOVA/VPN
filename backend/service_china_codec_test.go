package main

import (
	"reflect"
	"testing"
)

func chinaCodecState() serviceChinaState {
	return serviceChinaState{Status: chinaIPStatus{Available: true, Entries: 1234, UpdatedAt: "stamp", Source: string([]byte{255, 0}), Error: "message"}, Fetching: true, Probed: true, RuleOK: true, Active: true}
}
func TestServiceStateChinaCodec(t *testing.T) {
	for _, v := range []serviceChinaState{{}, chinaCodecState()} {
		b, e := encodeServiceChinaState(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceChinaState(b)
		if e != nil || !reflect.DeepEqual(got, v) {
			t.Fatal("roundtrip", e)
		}
		for i := 1; i < len(b); i++ {
			if _, e := decodeServiceChinaState(b[:i]); e == nil {
				t.Fatal("truncated", i)
			}
		}
		if _, e := decodeServiceChinaState(b + "x"); e == nil {
			t.Fatal("trailing")
		}
	}
	for k := uint8(1); k <= 5; k++ {
		c := serviceChinaCommand{Action: k, Status: chinaCodecState().Status, Flag: true}
		b, e := encodeServiceChinaCommand(c)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServiceChinaCommand(b)
		if e != nil || got != c {
			t.Fatal("command", e)
		}
	}
	if _, e := encodeServiceChinaState(serviceChinaState{Status: chinaIPStatus{Active: true}}); e == nil {
		t.Fatal("view flags stored")
	}
}
