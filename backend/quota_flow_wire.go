package main

import (
	"encoding/binary"
	"errors"
)

type quotaFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type quotaFlowStart func([]byte) (quotaFlowSession, error)
type quotaFrame struct {
	kind, code          byte
	index               int64
	status              int
	url, proxy, message string
	info                *subscriptionUserInfo
	timestamp           int64
}
type quotaReader struct {
	b   []byte
	p   int
	bad bool
}

func (r *quotaReader) take(n int) []byte {
	if n < 0 || n > len(r.b)-r.p {
		r.bad = true
		return nil
	}
	v := r.b[r.p : r.p+n]
	r.p += n
	return v
}
func (r *quotaReader) byte() byte {
	v := r.take(1)
	if len(v) != 1 {
		return 0
	}
	return v[0]
}
func (r *quotaReader) integer() int64 {
	v := r.take(8)
	if len(v) != 8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(v))
}
func (r *quotaReader) str() string {
	n := r.integer()
	if n < 0 || n > int64(len(r.b)-r.p) {
		r.bad = true
		return ""
	}
	return string(r.take(int(n)))
}
func (r *quotaReader) info() *subscriptionUserInfo {
	b := r.byte()
	if b == 0 {
		return nil
	}
	if b != 1 {
		r.bad = true
		return nil
	}
	return &subscriptionUserInfo{Upload: r.integer(), Download: r.integer(), Total: r.integer(), Expire: r.integer()}
}
func quotaPutInfo(b []byte, v *subscriptionUserInfo) []byte {
	b = registryBool(b, v != nil)
	if v != nil {
		for _, n := range []int64{v.Upload, v.Download, v.Total, v.Expire} {
			b = registryInt(b, n)
		}
	}
	return b
}
func quotaStartInput(q quotaServiceRequest) ([]byte, error) {
	if q.Op != "refresh" && q.Op != "error" || q.Status < 0 || q.Status > 65535 {
		return nil, errors.New("unsupported quota request")
	}
	b := registryBytes(nil, q.ID)
	b = registryBool(b, q.Op == "error")
	b = registryBytes(b, q.OriginalURL)
	b = registryBytes(b, q.Message)
	b = binary.LittleEndian.AppendUint16(b, uint16(q.Status))
	if len(b) > 8<<20 {
		return nil, errors.New("quota input limit")
	}
	return b, nil
}
func decodeQuotaFrame(b []byte) (quotaFrame, error) {
	r := quotaReader{b: b}
	f := quotaFrame{kind: r.byte(), index: -1}
	switch f.kind {
	case 1, 3, 4, 7, 8, 10, 12:
	case 2, 5:
		f.url = r.str()
	case 6:
		f.url = r.str()
		f.proxy = r.str()
	case 9:
		f.index = r.integer()
		f.info = r.info()
		f.timestamp = r.integer()
		f.code = r.byte()
		f.message = r.str()
	case 11:
		v := r.take(2)
		if len(v) == 2 {
			f.status = int(binary.LittleEndian.Uint16(v))
		}
		f.code = r.byte()
		f.index = r.integer()
		f.message = r.str()
	default:
		r.bad = true
	}
	if r.bad || r.p != len(r.b) {
		return f, errors.New("invalid quota frame")
	}
	return f, nil
}
func quotaMessage(code byte, message string) string {
	key := map[byte]string{1: "missing-id", 2: "not-found", 3: "no-remote", 4: "no-info", 5: "network", 6: "changed", 7: "deleted", 8: "save-info", 9: "save-error"}[code]
	if code == 10 {
		return message
	}
	return quotaServiceMessages[key]
}
func quotaPort(f quotaFrame, q quotaServiceRequest, original string, index int) (quotaEffect, bool) {
	e := quotaEffect{}
	switch f.kind {
	case 1, 7:
		e.Kind = "lock"
		e.ID = q.ID
	case 2:
		e.Kind = "categorize"
		e.URL = f.url
	case 3:
		e.Kind = "proxies"
	case 4:
		e.Kind = "unlock"
	case 5:
		e.Kind = "candidates"
		e.URL = f.url
	case 6:
		e.Kind = "fetch"
		e.URL = f.url
		e.Proxy = f.proxy
	case 8:
		e.Kind = "now"
	case 9:
		if f.index < 0 || f.index > 100000 {
			return e, false
		}
		e.Kind = "commit"
		e.Index = int(f.index)
		e.ID = q.ID
		e.OriginalURL = original
		e.Info = f.info
		e.Timestamp = f.timestamp
		e.Success = f.info != nil
		e.Message = quotaMessage(f.code, f.message)
		if e.Success {
			e.Message = q.Message
		}
	case 10:
		e.Kind = "save"
		e.Index = index
	default:
		return e, false
	}
	return e, true
}
func quotaReplyBytes(f quotaFrame, r quotaReply) ([]byte, error) {
	switch f.kind {
	case 1, 7:
		if len(r.Entries) > 100000 {
			return nil, errors.New("quota entries limit")
		}
		b := registryInt([]byte{1}, int64(len(r.Entries)))
		for _, e := range r.Entries {
			b = registryBytes(b, e.ID)
			b = registryBytes(b, e.URL)
		}
		if len(b) > 8<<20 {
			return nil, errors.New("quota reply limit")
		}
		return b, nil
	case 2, 3, 5:
		strings := r.Strings
		if f.kind == 5 {
			if len(strings) == 0 || strings[0] != f.url {
				return nil, errors.New("quota candidates mismatch")
			}
			strings = strings[1:]
		}
		b := profileStrings([]byte{2}, strings)
		if len(b) > 8<<20 {
			return nil, errors.New("quota strings limit")
		}
		return b, nil
	case 6:
		b := registryBool([]byte{3}, r.Err == nil)
		b = quotaPutInfo(b, r.Info)
		return registryBool(b, errors.Is(r.Err, errNoSubscriptionUserInfo)), nil
	case 8:
		return registryInt([]byte{4}, r.Timestamp), nil
	case 10:
		return registryBool([]byte{5}, r.Err == nil), nil
	default:
		return []byte{0}, nil
	}
}
