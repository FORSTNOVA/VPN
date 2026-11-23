package main

import "time"

type bootstrapServiceRequest struct {
	Op                           string
	Elevated, Enable, Authorized bool
	Wait, ShutdownWait           time.Duration
}
type bootstrapServiceEffect struct {
	Kind     string
	Count    int
	Value    int64
	Flag     bool
	Duration time.Duration
}
type bootstrapServiceReply struct {
	Err                              error
	Flag, Other, Elevated, Connected bool
	Count                            int
	Value, OtherValue                int64
}
type bootstrapServiceResult struct {
	Err               error
	Bool, Reuse, Lock bool
}
type bootstrapServiceEffects interface {
	Call(bootstrapServiceEffect) bootstrapServiceReply
	Release()
}
type bootstrapFinalizer interface {
	FinalizeBootstrapResult(bootstrapServiceRequest, bootstrapServiceResult) bootstrapServiceResult
}
