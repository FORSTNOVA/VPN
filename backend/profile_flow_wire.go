package main

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type profileFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type profileFlowStart func([]byte) (profileFlowSession, error)
type profileFrame struct {
	kind       byte
	a, b, c    int64
	data, text string
	items      []int64
}
type profileValue struct {
	info        *subscriptionUserInfo
	acquisition profileAcquisitionResult
	body        []byte
	nodes       []proxyNode
	source      SubscriptionSource
	config      string
}
type profileArena struct{ values []profileValue }

func (a *profileArena) put(v profileValue) int64 {
	a.values = append(a.values, v)
	return int64(len(a.values))
}
func (a *profileArena) get(n int64) (profileValue, bool) {
	if n <= 0 || n > int64(len(a.values)) {
		return profileValue{}, false
	}
	return a.values[n-1], true
}
func profileStrings(b []byte, v []string) []byte {
	b = registryInt(b, int64(len(v)))
	for _, s := range v {
		b = registryBytes(b, s)
	}
	return b
}
func profileIntegers(b []byte, v []int64) []byte {
	b = registryInt(b, int64(len(v)))
	for _, n := range v {
		b = registryInt(b, n)
	}
	return b
}
func profileStartInput(s profileServiceState, r profileServiceRequest, a *profileArena) ([]byte, error) {
	ops := map[string]byte{"cache-id": 0, "cache-active": 1, "use-cache": 2, "cached-sources": 3, "replace": 4, "write-merged": 5, "merge-caches": 6, "proxies": 7, "download": 8, "fetch": 9, "fetch-single": 10, "merge-all": 11, "refresh-single": 12, "merge": 13, "auto-tick": 14, "acquire-entry": 15}
	op, ok := ops[r.Op]
	if !ok {
		return nil, errors.New("unsupported profile operation")
	}
	if (r.Op != "acquire-entry" && len(s.Subscriptions) > 256) || len(s.ManualNodes) > 100000 {
		return nil, errors.New("profile snapshot limit")
	}
	entries := s.Subscriptions
	entryIndex := int64(-1)
	if r.Op == "acquire-entry" {
		entries = []subscriptionEntry{r.Entry}
		entryIndex = 0
	}
	b := registryInt(nil, int64(len(entries)))
	for _, e := range entries {
		for _, v := range []string{e.ID, e.URL, e.Label, e.Prefix, subscriptionHash(e.URL)} {
			b = registryBytes(b, v)
		}
		b = registryBool(b, e.Enabled)
		b = registryBool(b, strings.TrimSpace(e.URL) != "")
		b = registryInt(b, e.UpdatedAt)
	}
	for _, v := range []string{s.Settings.SubscriptionURL, subscriptionHash(s.Settings.SubscriptionURL), s.SelectedChoice, s.Settings.SelectedNode} {
		b = registryBytes(b, v)
	}
	manual := s.ManualNodes
	if r.Op == "acquire-entry" {
		manual = nil
	}
	names := make([]string, len(manual))
	for i, n := range manual {
		names[i] = n.Name
	}
	b = profileStrings(b, names)
	b = registryInt(b, int64(len(s.CachedNodes)))
	b = registryInt(b, int64(s.Settings.AutoMergeHours))
	mixed := ""
	if s.MixedPort > 0 {
		mixed = net.JoinHostPort("127.0.0.1", strconv.Itoa(s.MixedPort))
	}
	b = registryBytes(b, mixed)
	b = append(b, op)
	id := r.ID
	if r.Op == "refresh-single" {
		id = strings.TrimSpace(id)
	}
	b = registryBytes(b, id)
	body := a.put(profileValue{body: r.Body})
	b = registryInt(b, body)
	b = registryBool(b, r.Body == nil)
	b = registryBytes(b, r.Format)
	sourceInput := r.Sources
	if r.Op != "write-merged" {
		sourceInput = nil
	}
	sources := make([]int64, len(sourceInput))
	for i, v := range sourceInput {
		sources[i] = a.put(profileValue{source: v})
	}
	b = profileIntegers(b, sources)
	b = registryBytes(b, r.URL)
	b = registryInt(b, entryIndex)
	if len(b) > 8<<20 {
		return nil, errors.New("profile snapshot too large")
	}
	return b, nil
}
func decodeProfileFrame(b []byte) (profileFrame, error) {
	f := profileFrame{}
	p := 0
	take := func(n int) ([]byte, bool) {
		if n < 0 || n > len(b)-p {
			return nil, false
		}
		v := b[p : p+n]
		p += n
		return v, true
	}
	num := func() (int64, bool) {
		v, ok := take(8)
		if !ok {
			return 0, false
		}
		return int64(binary.LittleEndian.Uint64(v)), true
	}
	str := func() (string, bool) {
		n, ok := num()
		if !ok || n < 0 || n > int64(len(b)-p) {
			return "", false
		}
		v, ok := take(int(n))
		return string(v), ok
	}
	bad := errors.New("invalid profile frame")
	v, ok := take(1)
	if !ok {
		return f, bad
	}
	f.kind = v[0]
	if f.a, ok = num(); !ok {
		return f, bad
	}
	if f.b, ok = num(); !ok {
		return f, bad
	}
	if f.c, ok = num(); !ok {
		return f, bad
	}
	if f.data, ok = str(); !ok {
		return f, bad
	}
	if f.text, ok = str(); !ok {
		return f, bad
	}
	n, ok := num()
	if !ok || n < 0 || n > 100000 || n > int64((len(b)-p)/8) {
		return f, bad
	}
	for i := int64(0); i < n; i++ {
		x, _ := num()
		f.items = append(f.items, x)
	}
	if p != len(b) {
		return f, bad
	}
	return f, nil
}
func profileLogicalPath(s profileServiceState, r profileServiceRequest, f profileFrame) (string, bool) {
	profile := s.ProfilePath
	if r.Op == "replace" {
		profile = r.Path
	}
	cache := filepath.Join(s.Home, "subscriptions", f.data+".yaml")
	switch f.kind {
	case 1:
		if f.a == 1 {
			return filepath.Dir(cache), true
		}
		if f.a == 2 {
			return filepath.Dir(profile), true
		}
	case 2:
		if f.a == 1 {
			return cache, true
		}
		if f.a == 2 {
			return cache + ".format", true
		}
		if f.a == 3 {
			return profile, true
		}
		if f.a == 4 {
			return profile + ".source", true
		}
	case 3:
		switch f.a {
		case 1:
			return cache, true
		case 2:
			return cache + ".format", true
		case 3:
			return profile, true
		case 4:
			return profile + ".tmp", true
		case 5:
			return profile + ".source", true
		case 6:
			return profile + ".format", true
		}
	case 4:
		if f.a == 1 {
			return profile + ".tmp", true
		}
	case 5:
		if f.a == 1 {
			return profile + ".tmp", true
		}
		if f.a == 2 {
			return profile, true
		}
	}
	return "", false
}

func profilePort(s profileServiceState, r profileServiceRequest, f profileFrame, a *profileArena) (profileEffect, bool) {
	q := profileEffect{}
	path, pathOK := profileLogicalPath(s, r, f)
	switch f.kind {
	case 1:
		q.Kind = "mkdir"
		q.Path = path
		q.Mode = 0700
		return q, pathOK
	case 2:
		q.Kind = "read"
		q.Path = path
		return q, pathOK
	case 3:
		q.Kind = "write"
		q.Path = path
		q.Mode = 0600
		if f.b == 0 {
			q.Body = []byte(f.text)
		} else {
			v, ok := a.get(f.b)
			if !ok {
				return q, false
			}
			q.Body = v.body
		}
		return q, pathOK
	case 4:
		q.Kind = "rename"
		q.Path = path
		q.OtherPath = s.ProfilePath
		if r.Op == "replace" {
			q.OtherPath = r.Path
		}
		return q, pathOK
	case 5:
		q.Kind = "remove"
		q.Path = path
		return q, pathOK
	case 6:
		q.Kind = "parse"
		v, ok := a.get(f.b)
		q.Body = v.body
		return q, ok
	case 7:
		q.Kind = "merge-sources"
		for _, n := range f.items {
			v, ok := a.get(n)
			if !ok {
				return q, false
			}
			q.Sources = append(q.Sources, v.source)
		}
		return q, true
	case 8:
		q.Kind = "running"
		return q, true
	case 9:
		q.Kind = "prepare-live-config"
		v, ok := a.get(f.b)
		q.Body = v.body
		return q, ok
	case 10:
		q.Kind = "apply-live-config"
		v, ok := a.get(f.b)
		q.Format = v.config
		return q, ok
	case 11:
		q.Kind = "save-settings"
		return q, true
	case 12:
		q.Kind = "save-nodes"
		v, ok := a.get(f.b)
		q.Nodes = v.nodes
		return q, ok
	}
	return profileBPort(s, r, f, a)
}

type profileWireReply struct {
	ok, flag, nilValue bool
	token, count       int64
	errKind            byte
	error              string
	names              []string
	tokens             []int64
	info               int64
}

func profileReplyBytes(v profileWireReply) []byte {
	b := registryBool(nil, v.ok)
	b = registryBool(b, v.flag)
	b = registryBool(b, v.nilValue)
	b = registryInt(b, v.token)
	b = registryInt(b, v.count)
	b = append(b, v.errKind)
	b = registryBytes(b, v.error)
	b = profileStrings(b, v.names)
	b = profileIntegers(b, v.tokens)
	return registryInt(b, v.info)
}
func profileEncodeReply(f profileFrame, r profileReply, a *profileArena) []byte {
	v := profileWireReply{ok: r.Err == nil, flag: r.Bool, nilValue: r.Body == nil}
	if r.Err != nil {
		v.error = r.Err.Error()
		if errors.Is(r.Err, errNoUsableNodes) {
			v.errKind = 2
		} else if errors.Is(r.Err, errSubscriptionNetwork) {
			v.errKind = 3
		}
		if os.IsNotExist(r.Err) {
			v.errKind = 1
		}
	}
	switch f.kind {
	case 2:
		v.token = a.put(profileValue{body: r.Body})
		v.count = int64(len(r.Body))
		v.flag = len(r.Body) > 0
		v.names = []string{strings.TrimSpace(string(r.Body))}
	case 6:
		v.token = a.put(profileValue{nodes: append([]proxyNode(nil), r.Nodes...)})
		v.count = int64(len(r.Nodes))
		for _, n := range r.Nodes {
			v.names = append(v.names, n.Name)
		}
	case 7:
		v.token = a.put(profileValue{body: r.Body})
		v.info = a.put(profileValue{nodes: append([]proxyNode(nil), r.Nodes...)})
		v.count = int64(len(r.Nodes))
		for _, n := range r.Nodes {
			v.names = append(v.names, n.Name)
		}
	case 9:
		v.token = a.put(profileValue{config: r.Text})
	}
	if f.kind >= 14 {
		v = profileBReply(f, r, a, v)
	}
	return profileReplyBytes(v)
}
