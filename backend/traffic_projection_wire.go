package main

import (
	"encoding/binary"
	"errors"
)

const trafficProjectionMaxBytes = 64 << 20

func projectionPutString(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}
func projectionPutBool(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}
func trafficProjectionEncode(q trafficProjectionRequest) ([]byte, error) {
	if len(q.Rows) > 100000 {
		return nil, errors.New("projection row bound")
	}
	b := []byte{q.Op}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(q.Rows)))
	for _, f := range q.Rows {
		for _, s := range []string{f.Process, f.Path, f.Base, f.Host, f.IP, f.Port, f.HostPort, f.IPPort, f.UID, f.Network, f.Type, f.Rule, f.Payload} {
			b = projectionPutString(b, s)
		}
		if len(f.Chains) > 100000 {
			return nil, errors.New("chain bound")
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(f.Chains)))
		for _, s := range f.Chains {
			b = projectionPutString(b, s)
		}
		for _, v := range []int64{f.Upload, f.Download, f.Elapsed} {
			b = binary.LittleEndian.AppendUint64(b, uint64(v))
		}
		b = projectionPutBool(b, f.Parsed)
		if len(b) > trafficProjectionMaxBytes {
			return nil, errors.New("projection bytes bound")
		}
	}
	for _, s := range []string{q.Period, q.Date, q.Formatted} {
		b = projectionPutString(b, s)
	}
	b = projectionPutBool(b, q.Parsed)
	b = projectionPutString(b, q.Offset)
	b = projectionPutString(b, q.Limit)
	b = binary.LittleEndian.AppendUint64(b, uint64(q.OffsetValue))
	b = binary.LittleEndian.AppendUint64(b, uint64(q.LimitValue))
	b = projectionPutBool(b, q.OffsetOK)
	b = projectionPutBool(b, q.LimitOK)
	if len(b) > trafficProjectionMaxBytes {
		return nil, errors.New("projection bytes bound")
	}
	return b, nil
}

type projectionReader struct {
	b   []byte
	i   int
	bad bool
}

func (r *projectionReader) take(n int) []byte {
	if n < 0 || n > len(r.b)-r.i {
		r.bad = true
		return nil
	}
	x := r.b[r.i : r.i+n]
	r.i += n
	return x
}
func (r *projectionReader) u32() int {
	b := r.take(4)
	if len(b) != 4 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(b))
}
func (r *projectionReader) i64() int64 {
	b := r.take(8)
	if len(b) != 8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}
func (r *projectionReader) str() string { return string(r.take(r.u32())) }
func trafficProjectionDecode(b []byte) (trafficProjectionResult, error) {
	r := projectionReader{b: b}
	v := trafficProjectionResult{}
	tag := r.take(1)
	if len(tag) == 1 {
		v.Code = int(tag[0])
	}
	v.Text = r.str()
	v.Other = r.str()
	a, c := r.i64(), r.i64()
	v.Offset = int(a)
	v.Limit = int(c)
	if int64(v.Offset) != a || int64(v.Limit) != c {
		r.bad = true
	}
	present := r.take(1)
	if len(present) != 1 || present[0] > 1 {
		r.bad = true
	}
	n := r.u32()
	if n > 100000 || n > (len(r.b)-r.i)/48 {
		return v, errors.New("projection output bound")
	}
	if len(present) == 1 && present[0] == 1 {
		v.Rows = make([]connectionRow, 0, n)
	} else if n != 0 {
		r.bad = true
	}
	for i := 0; i < n; i++ {
		v.Rows = append(v.Rows, connectionRow{Process: r.str(), Target: r.str(), Network: r.str(), Type: r.str(), Rule: r.str(), Chain: r.str(), Upload: r.i64(), Download: r.i64(), DurationMs: r.i64()})
	}
	if r.bad || r.i != len(b) || v.Code > 6 {
		return v, errors.New("invalid projection output")
	}
	return v, nil
}
