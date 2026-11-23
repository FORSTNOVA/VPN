package main

type labelsServiceRequest struct{ Op string }
type labelsServiceResult struct{ Err error }
type labelsSource struct {
	Enabled, Nonempty, Matches bool
	Label                      string
}
type labelsNode struct {
	Name, Label string
	Manual      bool
}
type labelsState struct {
	Merged, Empty bool
	DefaultLabel  string
	Sources       []labelsSource
	Nodes         []labelsNode
}
type labelsEffect struct {
	Kind, Name, Text string
	Index            int
}
type labelsReply struct {
	Err         error
	Bool, Other bool
	Count       int
	Text        string
	State       *labelsState
}
type labelsServiceEffects interface {
	Call(labelsEffect) labelsReply
	Release()
}

func cloneLabelsState(s *labelsState) *labelsState {
	if s == nil {
		return nil
	}
	c := *s
	c.Sources = append([]labelsSource(nil), s.Sources...)
	c.Nodes = append([]labelsNode(nil), s.Nodes...)
	return &c
}
