package main

// Tokens are invocation-local Go resources. Native receives only decision facts;
// arbitrary decoded maps, byte slices and credential values never become mirrors.
type converterServiceRequest struct {
	Op      string
	Body    int
	Sources []converterSource
	Name    string
}
type converterSource struct {
	Body          int
	Label, Prefix string
}
type converterField struct {
	Key               string
	Ref, TrimRef      int
	Text, Trim, Lower string
	Empty, TrimEmpty  bool
	Bool              bool
	Int               int64
	Count             int
}
type converterServiceEffect struct {
	Kind              string
	Token, Ref, Index int
	Key, Text         string
	Value             int64
	Flag              bool
	Tokens            []int
	Fields            []converterAssignment
	Node              converterNode
}
type converterAssignment struct {
	Key   string
	Ref   int
	Text  string
	Value int64
	Kind  int // 0 resource reference, 1 string, 2 integer, 3 bool (Value != 0)
}
type converterNode struct {
	Name, Type, Network, SourceLabel string
	TLS, WsHost, WsPath              bool
}
type converterServiceReply struct {
	Err          error
	Token, Count int
	Text         string
	Flag         bool
	Tokens       []int
	Fields       []converterField
	Node         converterNode
	Names        []string
}
type converterServiceResult struct {
	Err                  error
	Body, Proxies, Nodes int
	Text                 string
}
type converterServiceEffects interface {
	Call(converterServiceEffect) converterServiceReply
	Release()
}
