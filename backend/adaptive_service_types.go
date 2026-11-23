package main

import "time"

type adaptiveServiceRequest struct {
	Op              string
	Index, From, To int
}
type adaptiveServiceEffect struct {
	Kind         string
	Index, Count int
	Value        int64
	Flag         bool
	Duration     time.Duration
}
type adaptiveServiceReply struct {
	Err                                error
	Flag, Other                        bool
	Index, Count, Code, QCount, ACount int
	Value                              int64
	Bits                               uint64
	Text                               string
}
type adaptiveServiceResult struct {
	Err   error
	Bool  bool
	Value int64
}
type adaptiveServiceEffects interface {
	Call(adaptiveServiceEffect) adaptiveServiceReply
	Release()
}
type adaptiveFinalizer interface {
	FinalizeAdaptiveResult(adaptiveServiceRequest, adaptiveServiceResult) adaptiveServiceResult
}
