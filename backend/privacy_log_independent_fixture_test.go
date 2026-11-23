package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type privacyIndependentFX struct {
	Body    []byte
	Names   []string
	Fail    string
	Trace   []privacyLogEffect
	Written []byte
	Renamed bool
}

func (f *privacyIndependentFX) Release() {}
func (f *privacyIndependentFX) Call(e privacyLogEffect) (r privacyLogReply) {
	f.Trace = append(f.Trace, e)
	if e.Kind == f.Fail {
		r.Err = bootIndependentErr
	}
	switch e.Kind {
	case "secrets":
		r.Names = f.Names
	case "write-source":
		r.Names = f.Names
		r.Bytes = f.Body
	case "read":
		r.Bytes = f.Body
	case "write-temp", "sink":
		f.Written, _ = hex.DecodeString(e.Text)
	case "rename":
		if r.Err == nil {
			f.Renamed = true
		}
	case "log-error", "log-summary":
	default:
		panic("unsafe privacy port " + e.Kind)
	}
	return
}

type privacyIndependentRunner func(privacyLogRequest, privacyLogEffects) privacyLogResult

func privacyIndependentMatrix(t *testing.T, run privacyIndependentRunner) {
	for i := 0; i < 1200; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			q := privacyLogRequest{Op: []string{"secrets", "scrub", "write"}[i%3]}
			short := "abcdefghijkl"
			names := []string{hex.EncodeToString([]byte(short)), hex.EncodeToString([]byte(short + "long")), hex.EncodeToString([]byte("中文中文")), hex.EncodeToString([]byte{255, 0}), hex.EncodeToString([]byte(short))}
			if i%9 == 0 {
				names = append(names, "")
			}
			body := append([]byte{255, 0}, []byte(strings.Repeat(short+"long \n", i%7))...)
			a := &privacyIndependentFX{Body: body, Names: names, Fail: []string{"", "read", "write-temp", "rename", "sink"}[(i/3)%5]}
			b := *a
			ga := runGoPrivacyLog(q, a)
			gb := run(q, &b)
			if !reflect.DeepEqual(ga, gb) || !reflect.DeepEqual(a, &b) {
				t.Fatalf("%s seed%d\ngo=%+v actual=%+v\nGO=%+v\nACT=%+v", q.Op, i, ga, gb, a, &b)
			}
		})
	}
}
func privacyIndependentGoldens(t *testing.T, run privacyIndependentRunner) {
	short := "abcdefghijkl"
	long := short + "long"
	names := []string{hex.EncodeToString([]byte(short)), hex.EncodeToString([]byte(long))}
	for _, fail := range []string{"", "write-temp", "rename", "read"} {
		f := &privacyIndependentFX{Body: append([]byte{255}, []byte(long+" "+short)...), Names: names, Fail: fail}
		r := run(privacyLogRequest{Op: "scrub"}, f)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		var kinds []string
		for _, e := range f.Trace {
			kinds = append(kinds, e.Kind)
		}
		want := []string{"secrets", "read"}
		if fail != "read" {
			want = append(want, "write-temp")
			if fail != "write-temp" {
				want = append(want, "rename")
			}
			if fail == "" {
				want = append(want, "log-summary")
			} else {
				want = append(want, "log-error")
			}
			if !bytes.Equal(f.Written, append([]byte{255}, []byte("<redacted>long <redacted>")...)) {
				t.Fatalf("overlap bytes %q", f.Written)
			}
			if f.Trace[2].Number != 0600 {
				t.Fatal("mode")
			}
		}
		if !reflect.DeepEqual(kinds, want) || f.Renamed != (fail == "") {
			t.Fatalf("failure sequence %s %v", fail, kinds)
		}
	}
	// Counts in dropped bytes are independent, unlike sequential redaction in retained bytes.
	body := append([]byte(long+"\n"), bytes.Repeat([]byte("x"), logScrubCap)...)
	f := &privacyIndependentFX{Body: body, Names: names}
	run(privacyLogRequest{Op: "scrub"}, f)
	last := f.Trace[len(f.Trace)-1]
	if last.Kind != "log-summary" || !strings.Contains(last.Text, "held 2 subscription URL(s)") || !strings.Contains(last.Text, "redacted 0") {
		t.Fatalf("drop count %v", last)
	}
}
func TestPrivacyLogServiceIndependentMatrix(t *testing.T) {
	privacyIndependentMatrix(t, runSelectedPrivacyLog)
}
func TestPrivacyLogServiceIndependentGoldens(t *testing.T) {
	privacyIndependentGoldens(t, runSelectedPrivacyLog)
}
