package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func profileBPort(s profileServiceState, r profileServiceRequest, f profileFrame, a *profileArena) (profileEffect, bool) {
	q := profileEffect{}
	switch f.kind {
	case 14:
		q.Kind = "proxy-state"
	case 17:
		q.Kind = "download"
		q.URL = f.data
		q.Proxy = f.text
	case 18:
		q.Kind = "ensure-yaml"
		v, ok := a.get(f.b)
		if !ok {
			return q, false
		}
		q.Body = v.body
	case 19:
		q.Kind = "merge-profiles"
		for _, n := range f.items {
			v, ok := a.get(n)
			if !ok {
				return q, false
			}
			q.Parts = append(q.Parts, v.body)
		}
	case 21:
		q.Kind = "registry"
		op := "record-use"
		if f.a == 2 {
			op = "record-error"
		}
		if f.a != 1 && f.a != 2 {
			return q, false
		}
		q.Registry = subscriptionServiceRequest{Op: op, ID: f.data, NodeCount: int(f.c), Failure: f.text}
		if len(f.items) > 0 && f.items[0] != 0 {
			v, ok := a.get(f.items[0])
			if !ok {
				return q, false
			}
			q.Registry.Info = v.info
		}
	case 22:
		q.Kind = "now"
	case 24:
		q.Kind = "acquire-batch"
		for _, i := range f.items {
			if i < 0 || i >= int64(len(s.Subscriptions)) {
				return q, false
			}
			q.Entries = append(q.Entries, s.Subscriptions[i])
		}
	case 26:
		q.Kind = "log"
		q.Format = f.text
		if f.c == 1 {
			q.Format = "auto-merge failed: " + profileErrorText(f.a, f.data, f.text, s.SelectedChoice)
		}
	default:
		return q, false
	}
	return q, true
}
func profileBReply(f profileFrame, r profileReply, a *profileArena, v profileWireReply) profileWireReply {
	switch f.kind {
	case 14:
		v.names = []string{strings.TrimSpace(r.Text)}
	case 17, 18, 19:
		v.token = a.put(profileValue{body: r.Body})
		v.info = a.put(profileValue{nodes: append([]proxyNode(nil), r.Nodes...)})
		v.count = int64(len(r.Nodes))
		v.flag = len(r.Body) > 0
		for _, n := range r.Nodes {
			v.names = append(v.names, n.Name)
		}
		quota := int64(0)
		if r.Info != nil {
			copy := *r.Info
			quota = a.put(profileValue{info: &copy})
		}
		v.tokens = []int64{quota}
	case 22:
		v.count = r.Timestamp
	case 24:
		for _, x := range r.Results {
			copy := subscriptionCloneValue(reflect.ValueOf(x)).Interface().(profileAcquisitionResult)
			v.tokens = append(v.tokens, a.put(profileValue{acquisition: copy}))
		}
	}
	return v
}
func profilePureFrame(f profileFrame, s profileServiceState, r profileServiceRequest, a *profileArena) ([]byte, bool) {
	v := profileWireReply{ok: true}
	switch f.kind {
	case 15:
		remote, inline := categorizeSubscriptionInput(f.data)
		v.names = remote
		v.count = int64(len(inline))
		if len(inline) > 0 {
			v.token = a.put(profileValue{body: []byte(strings.Join(inline, "\n"))})
		}
	case 16:
		if alt, ok := clashFormatURL(f.data); ok {
			v.names = []string{alt}
		}
	case 23:
		v.token = a.put(profileValue{nodes: append([]proxyNode(nil), s.CachedNodes...)})
		v.count = int64(len(s.CachedNodes))
	case 25:
		x, ok := a.get(f.b)
		if !ok {
			return nil, false
		}
		ar := x.acquisition
		pr := profileReply{Body: ar.Body, Nodes: ar.Nodes, Info: ar.Info, Err: ar.Err}
		v.ok = pr.Err == nil
		if pr.Err != nil {
			v.error = pr.Err.Error()
		}
		v = profileBReply(profileFrame{kind: 17}, pr, a, v)
	default:
		return nil, false
	}
	return profileReplyBytes(v), true
}
func profileErrorText(code int64, data, text, selected string) string {
	if code == 21 {
		return errNoUsableNodes.Error()
	}
	if code == 22 {
		return errSubscriptionNetwork.Error()
	}
	if code >= 1 && code <= 9 {
		return profileKnownError(code)
	}
	if code == 30 {
		return "missing subscription id"
	}
	if code >= 23 && code <= 29 {
		return map[int64]string{23: "set a subscription URL first", 24: "could not create the local profile directory", 25: "could not save the local profile", 26: "could not update the local profile", 27: "could not save the profile source", 28: "could not save the profile format", 29: "could not save the fetched node list"}[code]
	}
	if code == 10 {
		return text
	}
	if code == 11 {
		return fmt.Sprintf(profileSelectedErrorFormat, selected)
	}
	if code == 12 {
		v := fmt.Sprintf(profileApplyErrorFormat, text)
		if data != "" {
			v += "\n" + data
		}
		return v
	}
	return text
}

const profileSelectedErrorFormat = "合并会移除当前线路 %s，请勾选其订阅或先切换线路"
const profileApplyErrorFormat = "内核未确认合并配置：%s"

func profileBResultMatches(f profileFrame, r profileServiceResult, a *profileArena, op, selected string, final profileServiceState, request profileServiceRequest) bool {
	if op == "auto-tick" {
		return f.a == 0 && r.Err == nil && r.Status == 200
	}
	if op == "proxies" {
		var names []string
		b := []byte(f.data)
		for len(b) > 0 {
			if len(b) < 8 {
				return false
			}
			n := binary.LittleEndian.Uint64(b)
			b = b[8:]
			if n > uint64(len(b)) {
				return false
			}
			names = append(names, string(b[:n]))
			b = b[n:]
		}
		return f.a == 0 && r.Err == nil && reflect.DeepEqual(names, r.Proxies)
	}
	if op == "acquire-entry" {
		body, quota := profileValue{}, profileValue{}
		var ok bool
		if len(f.items) < 2 {
			return false
		}
		if f.items[0] != 0 {
			body, ok = a.get(f.items[0])
			if !ok {
				return false
			}
		}
		if f.items[1] != 0 {
			quota, ok = a.get(f.items[1])
			if !ok {
				return false
			}
		}
		nodes := profileValue{}
		if f.b != 0 {
			nodes, ok = a.get(f.b)
			if !ok {
				return false
			}
		}
		errText := ""
		if r.Acquisition.Err != nil {
			errText = r.Acquisition.Err.Error()
		}
		valid := r.Err == nil && r.Acquisition.ID == request.Entry.ID && reflect.DeepEqual(body.body, r.Acquisition.Body) && reflect.DeepEqual(nodes.nodes, r.Acquisition.Nodes) && reflect.DeepEqual(quota.info, r.Acquisition.Info) && f.text == errText && f.c == int64(len(r.Acquisition.Nodes))
		if !valid {
			profileFlowRecoveryReason.Store(fmt.Sprintf("acquire body=%v nodes=%v quota=%v error=%v count=%v", reflect.DeepEqual(body.body, r.Acquisition.Body), reflect.DeepEqual(nodes.nodes, r.Acquisition.Nodes), reflect.DeepEqual(quota.info, r.Acquisition.Info), f.text == errText, f.c == int64(len(r.Acquisition.Nodes))))
		}
		return valid
	}
	status := 200
	if op == "merge" || op == "refresh-single" {
		if f.a != 0 {
			status = 502
		}
		if op == "refresh-single" && strings.TrimSpace(request.ID) == "" {
			status = 400
		}
	}
	if status != r.Status {
		return false
	}
	if f.a != 0 {
		if r.Err == nil {
			return false
		}
		want := profileErrorText(f.a, f.data, f.text, selected)
		if f.a == 21 {
			return errors.Is(r.Err, errNoUsableNodes)
		}
		if f.a == 22 {
			return errors.Is(r.Err, errSubscriptionNetwork)
		}
		return r.Err.Error() == want
	}
	if r.Err != nil {
		return false
	}
	if op == "fetch-single" || op == "refresh-single" {
		if len(f.items) < 3 {
			return false
		}
		i := f.items[2]
		if i < 0 || i >= int64(len(final.Subscriptions)) || r.Entry == nil {
			return false
		}
		if !reflect.DeepEqual(*r.Entry, final.Subscriptions[i]) {
			return false
		}
		if op == "refresh-single" {
			return reflect.DeepEqual(r.Data, map[string]any{"ok": true, "subscription": r.Entry})
		}
		return true
	}
	var nodes []proxyNode
	if f.b != 0 {
		v, ok := a.get(f.b)
		if !ok {
			return false
		}
		nodes = v.nodes
	}
	if !reflect.DeepEqual(nodes, r.Nodes) || f.c != int64(len(r.Nodes)) {
		return false
	}
	if op == "download" {
		if len(f.items) < 2 {
			return false
		}
		var body []byte
		var info *subscriptionUserInfo
		if f.items[0] != 0 {
			v, ok := a.get(f.items[0])
			if !ok {
				return false
			}
			body = v.body
		}
		if f.items[1] != 0 {
			v, ok := a.get(f.items[1])
			if !ok {
				return false
			}
			info = v.info
		}
		return reflect.DeepEqual(body, r.Body) && reflect.DeepEqual(info, r.Info)
	}
	if op == "merge" {
		return reflect.DeepEqual(r.Data, map[string]any{"ok": true, "nodes": len(r.Nodes)})
	}
	return true
}
