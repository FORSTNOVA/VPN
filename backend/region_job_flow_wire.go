package main

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

type regionJobFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type regionJobFlowStart func([]byte) (regionJobFlowSession, error)
type regionJobFrame struct {
	kind              byte
	a, b              int64
	data, text, extra string
	names             []string
}

func regionJobStartInput(q regionJobServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"start": 0, "run": 1, "finish": 2}[q.Op]
	if !ok {
		return nil, errors.New("unknown region job op")
	}
	b := registryBool([]byte{op}, q.Refresh)
	b = registryBool(b, q.Err != nil)
	msg := ""
	token := int64(0)
	if q.Err != nil {
		msg = q.Err.Error()
		token = 1
	}
	b = registryBytes(b, msg)
	b = registryInt(b, token)
	return b, nil
}
func decodeRegionJobFrame(b []byte) (regionJobFrame, error) {
	r := quotaReader{b: b}
	f := regionJobFrame{kind: r.byte(), a: r.integer(), b: r.integer(), data: r.str(), text: r.str(), extra: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
		r.bad = true
	} else {
		f.names = make([]string, n)
		for i := range f.names {
			f.names[i] = r.str()
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid region job frame")
	}
	return f, nil
}

type regionJobArena struct {
	now    time.Time
	record regionRecord
	job    *regionJob
	next   int64
	errs   map[int64]error
}

func newRegionJobArena(q regionJobServiceRequest) *regionJobArena {
	return &regionJobArena{next: 1, errs: map[int64]error{1: q.Err}}
}
func (a *regionJobArena) replyBytes(f regionJobFrame, r regionJobReply) ([]byte, error) {
	if f.kind == 4 {
		a.now = r.Now
	}
	if f.kind == 6 && r.Job != nil {
		v := *r.Job
		a.job = &v
	}
	token := int64(0)
	msg := ""
	if r.Err != nil {
		a.next++
		token = a.next
		a.errs[token] = r.Err
		msg = r.Err.Error()
	}
	flag := r.Bool
	name := r.IP
	if f.kind == 11 {
		name, flag = r.Value.(string)
	}
	if f.kind == 15 {
		flag = r.Found
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, flag)
	b = registryBool(b, r.Record.Status == regionVerified)
	b = registryInt(b, int64(r.Age))
	b = registryInt(b, int64(r.TTL))
	b = registryInt(b, token)
	b = registryBytes(b, name)
	b = registryBytes(b, r.Country)
	b = registryBytes(b, msg)
	b = profileStrings(b, r.Names)
	b = registryInt(b, int64(len(r.Delays)))
	for k, v := range r.Delays {
		b = registryBytes(b, k)
		b = registryInt(b, int64(v))
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		for _, v := range []bool{s.Chain, s.Subscription, s.JobExists, s.JobRunning, s.Store} {
			b = registryBool(b, v)
		}
		b = profileStrings(b, s.Sources)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("region job reply limit")
	}
	return b, nil
}
func (a *regionJobArena) port(f regionJobFrame) (regionJobEffect, bool) {
	q := regionJobEffect{}
	switch f.kind {
	case 1:
		q.Kind = "lock"
	case 2:
		q.Kind = "unlock"
	case 3:
		q.Kind = "snapshot"
	case 4:
		q.Kind = "now"
		q.Name = map[int64]string{1: "start", 2: "record", 3: "finish"}[f.a]
		if q.Name == "" {
			return q, false
		}
	case 5:
		q.Kind = "create"
		q.Now = a.now
	case 6:
		q.Kind = "response"
	case 7:
		q.Kind = "spawn"
		q.Refresh = f.a != 0
	case 8:
		q.Kind = "running"
	case 9:
		q.Kind = "start"
	case 10:
		q.Kind = "stop"
	case 11:
		q.Kind = "get"
		q.Path = "/proxies/" + f.data
	case 12:
		q.Kind = "list"
	case 13:
		q.Kind = "delays"
	case 14:
		q.Kind = "total"
		q.Count = int(f.a)
	case 15:
		q.Kind = "lookup"
		q.Name = f.data
	case 16:
		q.Kind = "age"
		q.Name = f.data
	case 17:
		q.Kind = "put"
		q.Name = f.data
	case 18:
		q.Kind = "query"
		q.Index = int(f.a)
		q.Name = f.data
	case 19:
		status, ok := map[int64]string{0: regionUnreachable, 1: regionSingle, 2: regionConflict, 3: regionVerified}[f.a]
		if !ok {
			return q, false
		}
		detail, ok := regionJobFrameDetail(f.b, f.names)
		if !ok {
			return q, false
		}
		sources := append([]string{}, f.names...)
		if f.b == 1 || f.b == 2 {
			sources = nil
		}
		q.Kind = "memory"
		q.Record = regionRecord{Name: f.data, Country: f.text, ExitIP: f.extra, Status: status, Sources: sources, Detail: detail, VerifiedAt: a.now}
		a.record = q.Record
	case 20:
		q.Kind = "store"
		q.Record = a.record
	case 21:
		q.Kind = "log"
		if f.a == 1 {
			q.Message = fmt.Sprintf("could not persist the exit region of %s: %s", f.data, f.text)
		} else if f.a == 2 {
			q.Message = "could not restore the node after an exit region check: " + f.text
		} else {
			return q, false
		}
	case 22:
		q.Kind = "increment"
		var ok bool
		q.Name, ok = map[int64]string{0: "skip", 1: "failed", 2: "verified"}[f.a]
		if !ok {
			return q, false
		}
	case 23:
		q.Kind = "finish"
		q.Now = a.now
		if f.a != 0 {
			q.Err = a.errs[f.b]
			if q.Err == nil || q.Err.Error() != f.text {
				return q, false
			}
		}
	default:
		return q, false
	}
	return q, true
}
func (a *regionJobArena) resultMatches(f regionJobFrame, r regionJobServiceResult) bool {
	if r.Err != nil {
		return false
	}
	if f.b == 4 {
		return r.Status == 0 && r.Message == "" && r.Job == nil
	}
	if f.a != int64(r.Status) {
		return false
	}
	if f.b == 0 {
		return r.Message == "" && reflect.DeepEqual(r.Job, a.job)
	}
	msg := map[int64]string{1: regionJobChainMessage(), 2: "set a subscription URL first", 3: "an exit region check is already running"}[f.b]
	return msg != "" && r.Message == msg && r.Job == nil
}

func regionJobFrameDetail(code int64, names []string) (string, bool) {
	switch code {
	case 1:
		return regionJobDelayMessage(), true
	case 2:
		return regionJobPutMessage(), true
	case 3:
		return "所有出口查询服务均未响应", true
	case 4:
		return "仅一个数据源响应，未经交叉核验", true
	case 5:
		if len(names) < 2 {
			return "", false
		}
		return names[0] + " 与 " + names[1] + " 报告的出口地区不一致", true
	case 6:
		return "两个数据源一致", true
	}
	return "", false
}
