package main

import (
	"encoding/binary"
	"errors"
)

type tunReportSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type tunReportStart func([]byte) (tunReportSession, error)

func tunReportBytes(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint64(b, uint64(len(s)))
	return append(b, s...)
}
func tunReportFlag(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}
func tunReportNames(b []byte, n []string) []byte {
	b = binary.LittleEndian.AppendUint64(b, uint64(len(n)))
	for _, s := range n {
		b = tunReportBytes(b, s)
	}
	return b
}
func tunReportInput(q tunReportRequest) []byte {
	return tunReportFlag(tunReportBytes(tunReportBytes(nil, q.Op), q.Name), q.IPv6)
}
func tunReportReplyBytes(r tunReportReply) []byte {
	b := tunReportFlag(nil, r.Flag)
	b = binary.LittleEndian.AppendUint64(b, uint64(int64(r.Number)))
	b = tunReportBytes(b, r.Text)
	b = tunReportNames(b, r.Names)
	b = tunReportNames(b, r.Other)
	b = tunReportFlag(b, r.Err != nil)
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	b = tunReportBytes(b, msg)
	b = tunReportFlag(b, r.Wintun.Installed)
	for _, v := range []string{r.Wintun.Path, r.Wintun.Version, r.Wintun.SHA256, r.Wintun.Signer, r.Wintun.Detail} {
		b = tunReportBytes(b, v)
	}
	b = tunReportBytes(b, r.Adapter.Name)
	b = tunReportFlag(b, r.Adapter.Present)
	b = tunReportBytes(b, r.Adapter.Status)
	b = tunReportFlag(b, r.Adapter.IPv4Route)
	b = tunReportFlag(b, r.Adapter.IPv6Route)
	b = tunReportNames(b, r.Adapter.RouteVia)
	for _, v := range []string{r.Exit.Address, r.Exit.Error, r.Region.Country, r.Region.ExitIP} {
		b = tunReportBytes(b, v)
	}
	b = tunReportFlag(b, r.Names == nil)
	b = tunReportFlag(b, r.Other == nil)
	return b
}
func tunReportFrame(b []byte) (kind, text, result string, err error) {
	get := func() (string, bool) {
		if len(b) < 8 {
			return "", false
		}
		n := binary.LittleEndian.Uint64(b)
		b = b[8:]
		if n > uint64(len(b)) {
			return "", false
		}
		s := string(b[:n])
		b = b[n:]
		return s, true
	}
	var ok bool
	if kind, ok = get(); !ok {
		return "", "", "", errors.New("bad tun frame")
	}
	if text, ok = get(); !ok {
		return "", "", "", errors.New("bad tun frame")
	}
	if result, ok = get(); !ok || len(b) != 0 {
		return "", "", "", errors.New("bad tun frame")
	}
	return
}
