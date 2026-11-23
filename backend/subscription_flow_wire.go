package main

import (
	"encoding/binary"
	"errors"
	"strings"
)

type subscriptionFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type subscriptionFlowStart func([]byte) (subscriptionFlowSession, error)
type registryEntry struct {
	id, url, label      string
	enabled, hasEnabled bool
}
type registryEffect struct {
	kind       byte
	index, aux int64
	data       string
	entries    []registryEntry
}

func registryInt(b []byte, v int64) []byte { return binary.LittleEndian.AppendUint64(b, uint64(v)) }
func registryBytes(b []byte, v string) []byte {
	b = registryInt(b, int64(len(v)))
	return append(b, []byte(v)...)
}
func registryBool(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}
func registryEntries(b []byte, entries []registryEntry) []byte {
	b = registryInt(b, int64(len(entries)))
	for _, e := range entries {
		b = registryBytes(b, e.id)
		b = registryBytes(b, e.url)
		b = registryBytes(b, e.label)
		b = registryBool(b, e.enabled)
		b = registryBool(b, e.hasEnabled)
	}
	return b
}
func registryMetadata(entries []subscriptionEntry) []registryEntry {
	v := make([]registryEntry, 0, len(entries))
	for _, e := range entries {
		v = append(v, registryEntry{e.ID, e.URL, e.Label, e.Enabled, true})
	}
	return v
}
func registrySnapshot(b []byte, s subscriptionServiceState) []byte {
	b = registryEntries(b, registryMetadata(s.Subscriptions))
	b = registryBytes(b, s.Settings.SubscriptionURL)
	b = registryBool(b, s.ProfileFromCache)
	return registryBool(b, s.StoreAvailable)
}
func registryStartInput(s subscriptionServiceState, r subscriptionServiceRequest) ([]byte, error) {
	ops := map[string]byte{"load": 0, "save": 1, "ensure": 2, "find": 3, "find-url": 4, "list": 5, "add": 6, "update": 7, "rename": 8, "record-use": 9, "record-error": 10, "hours": 11, "delete": 12, "activate": 13}
	op, ok := ops[r.Op]
	if !ok || len(s.Subscriptions) > 256 || len(s.Health)+len(s.Regions)+len(s.CachedNodes) > 4096 {
		return nil, errors.New("unsupported registry request")
	}
	if op == 5 {
		n := 0
		for _, e := range s.Subscriptions {
			if e.URL == s.Settings.SubscriptionURL {
				n++
			}
		}
		if n > 1 {
			return nil, errors.New("legacy duplicate-active ordering")
		}
	}
	if op == 0 {
		r.URL = strings.TrimSpace(s.Settings.SubscriptionURL)
		r.URLHash = subscriptionHash(r.URL)
	}
	b := registrySnapshot(nil, s)
	b = append(b, op)
	for _, v := range []string{r.ID, r.URL, r.Label, r.Prefix, r.URLHash, r.DefaultLabel} {
		b = registryBytes(b, v)
	}
	var presence uint16
	if r.HasEnabled {
		presence |= 1
	}
	if r.HasPrefix {
		presence |= 2
	}
	b = binary.LittleEndian.AppendUint16(b, presence)
	b = registryBool(b, r.Enabled)
	b = registryBool(b, r.URLValidationError == "")
	b = registryBool(b, r.Info != nil)
	b = registryInt(b, int64(r.Hours))
	b = registryInt(b, int64(r.NodeCount))
	b = registryInt(b, int64(r.LabelRunes))
	if len(b) > 8<<20 {
		return nil, errors.New("registry snapshot too large")
	}
	return b, nil
}

type registryReader struct {
	b   []byte
	p   int
	bad bool
}

func (d *registryReader) take(n int) []byte {
	if n < 0 || n > len(d.b)-d.p {
		d.bad = true
		return nil
	}
	v := d.b[d.p : d.p+n]
	d.p += n
	return v
}
func (d *registryReader) byte() byte {
	b := d.take(1)
	if len(b) == 0 {
		return 0
	}
	return b[0]
}
func (d *registryReader) boolean() bool {
	v := d.byte()
	if v > 1 {
		d.bad = true
	}
	return v == 1
}
func (d *registryReader) integer() int64 {
	b := d.take(8)
	if len(b) < 8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}
func (d *registryReader) text() string {
	n := d.integer()
	if n < 0 || n > 8<<20 {
		d.bad = true
		return ""
	}
	return string(d.take(int(n)))
}
func decodeRegistryEffect(b []byte) (registryEffect, error) {
	d := registryReader{b: b}
	e := registryEffect{kind: d.byte(), index: d.integer(), aux: d.integer(), data: d.text()}
	n := d.integer()
	if n < 0 || n > 100000 {
		d.bad = true
	} else {
		for i := int64(0); i < n && !d.bad; i++ {
			e.entries = append(e.entries, registryEntry{id: d.text(), url: d.text(), label: d.text(), enabled: d.boolean(), hasEnabled: d.boolean()})
		}
	}
	if d.bad || d.p != len(b) || len(b) > 8<<20 {
		return e, errors.New("invalid registry frame")
	}
	return e, nil
}
func registryReplyBytes(kind byte, r subscriptionEffectReply, s subscriptionServiceState) []byte {
	b := registryBool(nil, r.Err == nil)
	value := int64(r.Count)
	if kind == 9 || (kind == 8 && s.ProfileFromCache) {
		value = int64(len(s.CachedNodes))
	}
	if kind == 19 {
		value = r.Timestamp
	}
	b = registryInt(b, value)
	b = registryBool(b, r.Bool)
	text := r.Text
	if r.Err != nil {
		text = r.Err.Error()
	}
	b = registryBytes(b, text)
	var entries []registryEntry
	if kind == 1 {
		for _, e := range r.Loaded {
			entries = append(entries, registryEntry{e.Entry.ID, e.Entry.URL, e.Entry.Label, e.Entry.Enabled, e.HasEnabled})
		}
	} else if kind == 3 {
		entries = []registryEntry{{r.Entry.ID, r.Entry.URL, r.Entry.Label, r.Entry.Enabled, true}}
	}
	b = registryEntries(b, entries)
	composite := kind >= 8 && kind <= 11
	b = registryBool(b, composite)
	if composite {
		b = registrySnapshot(b, s)
	}
	return b
}
func registryAck() []byte {
	return registryReplyBytes(0, subscriptionEffectReply{}, subscriptionServiceState{})
}
func registryPort(e registryEffect) (string, string, bool) {
	names := []string{"", "read-registry", "open-secret", "describe-url", "seal-secret", "write-registry", "running", "profile-matches", "fetch", "use-cache", "merge", "merge-caches", "save-settings", "remove-nodes", "clear-health", "clear-regions", "remove-config", "remove-profile", "remove-cache", "now", "start-loop"}
	if e.kind < 1 || e.kind > 20 {
		return "", "", false
	}
	arg := ""
	switch e.kind {
	case 2, 3, 4, 9:
		arg = e.data
	case 17:
		if e.aux < 0 || e.aux > 2 {
			return "", "", false
		}
		arg = []string{"", ".source", ".format"}[e.aux]
	case 18:
		if e.aux < 0 || e.aux > 1 {
			return "", "", false
		}
		arg = e.data
		if e.aux == 1 {
			return "remove-cache-format", arg, true
		}
	}
	return names[e.kind], arg, true
}
