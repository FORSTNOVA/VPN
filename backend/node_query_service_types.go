package main

import "time"

type nodeQueryServiceRequest struct {
	Op       string
	Borrowed bool
	Timeout  time.Duration
	Target   string
}
type nodeQueryServiceResult struct {
	Err          error
	Status       int
	Message      string
	Nodes        []proxyNode
	Delays       map[string]int
	Selected     string
	Latency      int
	ProxyEnabled bool
}
type nodeQuerySource struct{ URL, Label string }
type nodeQueryMetadata struct {
	Cached, Manual            []proxyNode
	Sources                   []nodeQuerySource
	SubscriptionURL, Selected string
	ProxyEnabled              bool
	ConnectTimeout            time.Duration
}
type nodeQueryProvider struct {
	Valid               bool
	Name, Type, Network string
	Alive               bool
}
type nodeQueryEffect struct {
	Kind, Name, Text string
	Duration         time.Duration
	Time             time.Time
	Result           nodeQueryServiceResult
}
type nodeQueryReply struct {
	Value    int
	Err      error
	Bool     bool
	Text     string
	Now      time.Time
	Metadata *nodeQueryMetadata
	Nodes    []proxyNode
	Provider []nodeQueryProvider
	Delays   map[string]int
	Status   int
}
type nodeQueryServiceEffects interface {
	Call(nodeQueryEffect) nodeQueryReply
	Release()
}

func cloneNodeQueryMetadata(m *nodeQueryMetadata) *nodeQueryMetadata {
	if m == nil {
		return nil
	}
	n := *m
	n.Cached = append([]proxyNode(nil), m.Cached...)
	n.Manual = append([]proxyNode(nil), m.Manual...)
	n.Sources = append([]nodeQuerySource(nil), m.Sources...)
	return &n
}
