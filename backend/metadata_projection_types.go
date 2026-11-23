package main

type metadataProjectionRequest struct {
	Op, Path string
	Body     []byte
}
type metadataProjectionEffect struct {
	Kind, Text, Other string
	Number            int64
}
type metadataProjectionReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type metadataProjectionResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type metadataProjectionEffects interface {
	Call(metadataProjectionEffect) metadataProjectionReply
	Release()
}

func runMetadataProjectionWithEffects(q metadataProjectionRequest, fx metadataProjectionEffects) metadataProjectionResult {
	defer fx.Release()
	return runSelectedMetadataProjection(q, fx)
}
