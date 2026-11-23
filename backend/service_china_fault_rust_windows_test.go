//go:build windows && amd64 && rustcore

package main

import (
	"reflect"
	"testing"
)

func chinaFaultApply(t *testing.T, a *serviceAuthority, c serviceChinaCommand) {
	t.Helper()
	p, e := encodeServiceChinaCommand(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.mutate(serviceStateCommand{Kind: 7, ChinaPayload: p}); e != nil {
		t.Fatal(e)
	}
}
func TestServiceStateChinaDeliveryFaults(t *testing.T) {
	for k := uint8(1); k <= 5; k++ {
		for _, op := range []byte{3, 6} {
			a, tr, b := newServiceFault(t, op, false, true)
			c := serviceChinaCommand{Action: k, Status: chinaCodecState().Status, Flag: true}
			chinaFaultApply(t, a, c)
			raw, e := b.ChinaQuery()
			got, de := decodeServiceChinaState(raw)
			var want serviceChinaState
			_ = applyServiceChina(&want, c)
			if e != nil || de != nil || got != want {
				t.Fatal("state", k, e, de)
			}
			if tr.counts[3] != 1 || tr.counts[4] != 1 || tr.counts[6] != 1 {
				t.Fatal("replay", tr.counts)
			}
			_ = a.Close()
		}
	}
}

type chinaBadExport struct {
	serviceStateBackend
	field int
}

func (b *chinaBadExport) Export() (serviceStateCheckpoint, error) {
	cp, e := b.serviceStateBackend.Export()
	if e != nil {
		return cp, e
	}
	v, e := decodeServiceChinaState(cp.Snapshot.ChinaPayload)
	if e != nil {
		return cp, e
	}
	var f reflect.Value
	if b.field < 5 {
		f = reflect.ValueOf(&v.Status).Elem().Field(b.field)
	} else {
		f = reflect.ValueOf(&v).Elem().Field(b.field - 4)
	}
	switch f.Kind() {
	case reflect.Bool:
		f.SetBool(!f.Bool())
	case reflect.Int:
		f.SetInt(f.Int() + 1)
	case reflect.String:
		f.SetString(f.String() + "changed")
	}
	cp.Snapshot.ChinaPayload, e = encodeServiceChinaState(v)
	return cp, e
}
func TestServiceStateChinaCheckpointFields(t *testing.T) {
	for i := 0; i < 9; i++ {
		a, _, b := newServiceFault(t, 0, false, false)
		chinaFaultApply(t, a, serviceChinaCommand{Action: 1, Status: chinaCodecState().Status})
		bad := &chinaBadExport{b, i}
		a.backend = bad
		if e := a.transferToGo(); e == nil {
			t.Fatal("damaged checkpoint", i)
		}
		if a.backend != bad {
			t.Fatal("owner changed")
		}
		_ = a.Close()
	}
}
