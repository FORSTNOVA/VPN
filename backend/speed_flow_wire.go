package main

import (
	"errors"
	"math"
	"reflect"
	"time"
)

type speedFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type speedFlowStart func([]byte) (speedFlowSession, error)
type speedFrame struct {
	kind   byte
	effect speedServiceEffect
	eof    bool
}

func speedStartInput(q speedServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"handler": 0, "download": 1, "upload": 2, "down-stream": 3, "up-stream": 4, "zero-read": 5}[q.Op]
	if !ok {
		return nil, errors.New("unsupported speed op")
	}
	b := []byte{op}
	for _, v := range []int64{int64(q.MegaBytes), q.Allowance, int64(q.Budget), q.Remaining, int64(q.Length)} {
		b = registryInt(b, v)
	}
	return b, nil
}
func decodeSpeedFrame(b []byte) (speedFrame, error) {
	r := quotaReader{b: b}
	f := speedFrame{kind: r.byte()}
	e := &f.effect
	e.Upload = serviceReadBool(&r)
	e.Count = int(r.integer())
	e.Bytes = r.integer()
	e.Budget = time.Duration(r.integer())
	e.Status = int(r.integer())
	e.Text = r.str()
	e.Node = r.str()
	e.Temporary = serviceReadBool(&r)
	e.Result.MegaBytes = int(r.integer())
	e.Result.DownMbps = math.Float64frombits(uint64(r.integer()))
	e.Result.UpMbps = math.Float64frombits(uint64(r.integer()))
	e.Result.DownBytes = r.integer()
	e.Result.UpBytes = r.integer()
	e.Result.DownMs = r.integer()
	e.Result.UpMs = r.integer()
	e.Result.Error = r.str()
	if e.Result.MegaBytes != 0 {
		e.Result.Target = speedTestHost
	}
	f.eof = serviceReadBool(&r)
	return f, serviceWireDone(&r)
}

type speedArena struct{ q speedServiceRequest }

func newSpeedArena(q speedServiceRequest) *speedArena { return &speedArena{q: q} }
func (a *speedArena) replyBytes(f speedFrame, r speedServiceReply) ([]byte, error) {
	text := r.Text
	if r.Err != nil {
		text = r.Err.Error()
	}
	b := []byte{f.kind}
	b = registryBool(b, r.Err == nil)
	b = registryBool(b, r.Flag)
	b = registryBytes(b, text)
	for _, v := range []int64{r.Bytes, r.Started, int64(r.Elapsed), int64(math.Float64bits(r.Rate))} {
		b = registryInt(b, v)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("speed reply limit")
	}
	return b, nil
}
func (a *speedArena) internal(f speedFrame) ([]byte, error) {
	r := speedServiceReply{}
	switch f.effect.Count {
	case 1:
		r.Text = speedEmptyError(false).Error()
	case 2:
		r.Text = speedEmptyError(true).Error()
	case 3:
		r.Text = speedSelectionError().Error()
	default:
		return nil, errors.New("speed formatter code")
	}
	return a.replyBytes(f, r)
}
func (a *speedArena) resultMatches(f speedFrame, r speedServiceResult) bool {
	e := f.effect
	if a.q.Op == "download" || a.q.Op == "upload" {
		if (r.Err != nil) != (e.Status == 1) {
			return false
		}
		e.Status = 0
	}
	errText := ""
	if r.Err != nil {
		errText = r.Err.Error()
	}
	if errText != e.Text {
		return false
	}
	if a.q.Op == "handler" && (r.Err != nil) != (e.Status != 200) {
		return false
	}
	return r.Bytes == e.Bytes && r.Elapsed == e.Budget && r.EOF == f.eof && r.Status == e.Status && r.Node == e.Node && r.Temporary == e.Temporary && reflect.DeepEqual(r.Speed, e.Result)
}
func (a *speedArena) port(f speedFrame) (speedServiceEffect, bool) {
	e := f.effect
	k, ok := speedPortKinds[f.kind]
	e.Kind = k
	return e, ok
}

var speedPortKinds = map[byte]string{1: "lock", 2: "ensure", 3: "active", 4: "endpoint", 5: "measure-begin", 6: "batch-start", 7: "batch-join", 8: "totals", 9: "elapsed", 10: "measure-end", 11: "request-new", 12: "request-do", 13: "body-read", 14: "download-count", 15: "total-load", 16: "body-close", 17: "upload-drain", 18: "response", 19: "stop", 20: "unlock", 21: "zero-fill", 22: "zero-remaining", 23: "zero-count", 24: "rate"}
