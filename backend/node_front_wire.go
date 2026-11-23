package main

import (
	"encoding/binary"
	"errors"
)

type nodeFrontSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type nodeFrontStart func([]byte) (nodeFrontSession, error)

func nodeFrontInput(q nodeFrontRequest) []byte {
	return tunReportBytes(tunReportBytes(nil, q.Op), "")
}
func nodeFrontReplyBytes(r nodeFrontReply) []byte {
	b := tunReportBytes(nil, r.Text)
	b = binary.LittleEndian.AppendUint64(b, uint64(r.Number))
	b = tunReportFlag(b, r.Flag)
	b = tunReportNames(b, r.Names)
	b = tunReportBytes(b, string(r.Bytes))
	b = tunReportFlag(b, r.Err != nil)
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	return tunReportBytes(b, msg)
}
func nodeFrontResultJSON(r nodeFrontResult) map[string]any {
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	return map[string]any{"status": r.Status, "text": r.Text, "data": r.Data, "error": msg, "hasError": r.Err != nil}
}
func nodeFrontFrame(b []byte) (kind, text, other string, number int64, result string, err error) {
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
	bad := errors.New("invalid driver frame")
	var ok bool
	if kind, ok = get(); !ok {
		return "", "", "", 0, "", bad
	}
	if text, ok = get(); !ok {
		return "", "", "", 0, "", bad
	}
	if other, ok = get(); !ok || len(b) < 8 {
		return "", "", "", 0, "", bad
	}
	number = int64(binary.LittleEndian.Uint64(b))
	b = b[8:]
	if result, ok = get(); !ok || len(b) != 0 {
		return "", "", "", 0, "", bad
	}
	return
}

// These codec/IO failures are classified by presence only. Keep their original
// error objects in the Go continuation; do not copy a URL or source path into FFI.
func nodeFrontWireReply(kind string, r nodeFrontReply) nodeFrontReply {
	if r.Err != nil {
		switch kind {
		case "url-parse", "vmess-base64", "vmess-json", "yaml-facts", "convert-profile", "match-read", "match-source", "validate-url":
			r.Err = errors.New("opaque codec failure")
		}
	}
	return r
}
