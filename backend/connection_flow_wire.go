package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type connectionFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type connectionFlowStart func([]byte) (connectionFlowSession, error)
type connectionFrame struct {
	kind       byte
	a, b       int64
	data, text string
	items      []string
}

func connectionStartInput(q connectionServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"connect": 0, "disconnect": 1, "drop": 2, "select": 3}[q.Op]
	if !ok {
		return nil, errors.New("unknown connection operation")
	}
	b := registryBytes([]byte{op}, q.Name)
	b = registryBool(b, strings.TrimSpace(q.Name) != "")
	if len(b) > 8<<20 {
		return nil, errors.New("connection input limit")
	}
	return b, nil
}
func decodeConnectionFrame(b []byte) (connectionFrame, error) {
	r := quotaReader{b: b}
	f := connectionFrame{kind: r.byte(), a: r.integer(), b: r.integer(), data: r.str(), text: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
		r.bad = true
	} else {
		for i := int64(0); i < n; i++ {
			f.items = append(f.items, r.str())
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid connection frame")
	}
	return f, nil
}
func connectionCopyNodes(n []proxyNode) []proxyNode {
	if n == nil {
		return nil
	}
	return append([]proxyNode{}, n...)
}
func connectionReplyBytes(f connectionFrame, r connectionReply) ([]byte, error) {
	b := registryBool([]byte{f.kind}, r.Err == nil)
	flag := r.Bool
	name := ""
	names := r.Strings
	value := int64(r.Latency)
	switch f.kind {
	case 8:
		names = nil
		for _, n := range r.Nodes {
			names = append(names, n.Name)
		}
	case 10:
		name, flag = r.Value.(string)
	case 31:
		flag = r.Found
		name = r.Region.Country
		if r.Region.Status == regionVerified {
			value = 1
		} else {
			value = 0
		}
	}
	b = registryBool(b, flag)
	b = registryBool(b, errors.Is(r.Err, os.ErrProcessDone))
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	b = registryBytes(b, msg)
	b = registryBytes(b, name)
	b = profileStrings(b, names)
	b = registryInt(b, value)
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		b = registryBool(b, s.Chain)
		b = registryBool(b, s.ProxyOn)
		b = registryBool(b, s.StoreAvailable)
		for _, v := range []string{s.Mode, s.SelectionMode, s.SelectedNode, s.LockedRegion, s.LockedNode} {
			b = registryBytes(b, v)
		}
		cached := make([]string, len(s.CachedNodes))
		for i, n := range s.CachedNodes {
			cached[i] = n.Name
		}
		b = profileStrings(b, cached)
		b = registryBytes(b, s.Choice)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("connection reply limit")
	}
	return b, nil
}
func connectionPort(f connectionFrame, current *connectionServiceState, nodes []proxyNode) (connectionEffect, bool) {
	q := connectionEffect{}
	switch f.kind {
	case 1:
		q.Kind = "lock"
	case 2:
		q.Kind = "snapshot"
	case 3:
		q.Kind = "unlock"
	case 4:
		q.Kind = "running"
	case 5:
		q.Kind = "proxy-conflict"
	case 6:
		q.Kind = "start-kernel"
	case 7:
		q.Kind = "stop-kernel"
	case 8:
		q.Kind = "list"
	case 9:
		q.Kind = "put"
		q.Path = "/proxies/SmartVPN"
		q.Name = f.data
	case 10:
		q.Kind = "get"
		q.Path = "/proxies/" + f.data
	case 11:
		q.Kind = "save-settings"
	case 12:
		q.Kind = "commit"
		q.Mutation = "cache"
		q.Nodes = connectionCopyNodes(nodes)
		for i := range q.Nodes {
			q.Nodes[i].Selected = q.Nodes[i].Name == f.data
		}
	case 13:
		q.Kind = "save-cache"
		if current == nil {
			return q, false
		}
		q.Nodes = connectionCopyNodes(current.CachedNodes)
	case 14:
		q.Kind = "upsert"
		if current == nil {
			return q, false
		}
		q.Nodes = connectionCopyNodes(current.CachedNodes)
	case 15:
		q.Kind = "candidates"
		q.Region = f.data
	case 16:
		q.Kind = "region-fresh"
		q.Name = f.data
	case 17:
		q.Kind = "commit"
		q.Mutation = "region"
		q.Name = f.data
		q.Region = f.text
	case 18:
		q.Kind = "commit"
		q.Mutation = "lock-node"
		q.Name = f.data
	case 19:
		q.Kind = "commit"
		q.Mutation = "selection"
		q.Mode = f.data
		q.Name = f.text
	case 20:
		q.Kind = "commit"
		q.Mutation = "cache"
		if current == nil {
			return q, false
		}
		q.Nodes = connectionCopyNodes(current.CachedNodes)
		for i := range q.Nodes {
			q.Nodes[i].Selected = q.Nodes[i].Name == f.data
		}
	case 21:
		q.Kind = "commit"
		q.Mutation = "clear-block"
	case 22:
		q.Kind = "system-proxy"
		q.Bool = f.a != 0
	case 23:
		q.Kind = "commit"
		q.Mutation = "proxy-on"
		q.Bool = f.a != 0
	case 24:
		q.Kind = "start-patrol"
	case 25:
		q.Kind = "start-path"
	case 26:
		q.Kind = "async-patrol"
	case 27:
		q.Kind = "stop-patrol"
	case 28:
		q.Kind = "stop-path"
	case 29:
		q.Kind = "latency"
		q.Name = f.data
	case 30:
		q.Kind = "log"
		switch f.a {
		case 1:
			q.Name = fmt.Sprintf("could not persist the node list: %s", f.text)
		case 4:
			q.Name = fmt.Sprintf("could not persist the locked region: %s", f.text)
		case 2:
			q.Name = fmt.Sprintf("could not pin the group to %s: %s", f.data, f.text)
		case 3:
			q.Name = "no verified exit region; same-region failover stays off until a region is locked"
		default:
			return q, false
		}
	case 31:
		q.Kind = "region"
		q.Name = f.data
	default:
		return q, false
	}
	return q, true
}
