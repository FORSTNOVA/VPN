package main

import (
	"os"
	"runtime"
	"strconv"
)

type appMetadataProjectionEffects struct {
	a           *app
	body        []byte
	manual      []manualNode
	profiles    []map[string]any
	current     map[string]any
	details     map[string]proxyNode
	domains     []string
	index       map[string][]map[string]any
	detailFacts metadataProjectionReply
	readFile    func(string) ([]byte, error)
	hooks       map[string]func(metadataProjectionEffect) metadataProjectionReply
}

func (f *appMetadataProjectionEffects) Release() {}
func (f *appMetadataProjectionEffects) Call(e metadataProjectionEffect) metadataProjectionReply {
	if h := f.hooks[e.Kind]; h != nil {
		return h(e)
	}
	switch e.Kind {
	case "manual-count":
		return metadataProjectionReply{Number: int64(len(f.manual))}
	case "details-new":
		f.details = make(map[string]proxyNode, int(e.Number))
	case "detail-facts":
		node := f.manual[int(e.Number)]
		network, _ := node.Proxy["network"].(string)
		tls, _ := node.Proxy["tls"].(bool)
		options, valid := node.Proxy["ws-opts"].(map[string]any)
		path, _ := options["path"].(string)
		_, header := options["headers"]
		flags := int64(0)
		if valid {
			flags |= 1
		}
		if header {
			flags |= 2
		}
		f.detailFacts = metadataProjectionReply{Names: []string{node.Name, node.Type, network, path}, Flag: tls, Number: flags}
		return f.detailFacts
	case "details-put":
		bits, err := strconv.Atoi(e.Other)
		if err != nil {
			panic(err)
		}
		r := f.detailFacts
		f.details[e.Text] = proxyNode{Name: r.Names[0], Type: r.Names[1], Network: r.Names[2], TLS: bits&1 != 0, WsHost: bits&2 != 0, WsPath: bits&4 != 0, Manual: true}
	case "domains-new":
		f.domains = make([]string, 0, int(e.Number))
	case "suffix-child":
		s, ok := hostnameSuffix(f.manual[int(e.Number)].Server)
		return metadataProjectionReply{Text: s, Flag: ok}
	case "domain-append":
		f.domains = append(f.domains, e.Text)
	case "subscription":
		return metadataProjectionReply{Text: f.a.controlMetaLocked().SubscriptionURL}
	case "read-profile":
		read := f.readFile
		if read == nil {
			read = os.ReadFile
		}
		b, err := read(f.a.profilePath())
		f.body = b
		return metadataProjectionReply{Err: err}
	case "manual-snapshot":
		f.manual = f.a.manualSnapshotLocked()
	case "decode-profile":
		rows, err := profileIndexRows(f.body)
		f.profiles = rows
		return metadataProjectionReply{Number: int64(len(rows)), Err: err}
	case "index-new":
		f.index = make(map[string][]map[string]any)
	case "profile-name":
		f.current = f.profiles[int(e.Number)]
		return metadataProjectionReply{Text: stringValue(f.current["name"])}
	case "manual-name":
		f.current = f.manual[int(e.Number)].Proxy
		return metadataProjectionReply{Text: stringValue(f.current["name"])}
	case "index-append":
		f.index[e.Text] = append(f.index[e.Text], f.current)
	case "mode-read":
		return metadataProjectionReply{Text: f.a.controlMetaLocked().ConnectionMode}
	case "platform":
		return metadataProjectionReply{Text: runtime.GOOS}
	default:
		panic("unknown metadata projection port")
	}
	return metadataProjectionReply{}
}
