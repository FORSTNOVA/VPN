package main

import "time"

type speedServiceRequest struct {
	Op        string
	MegaBytes int
	Allowance int64
	Budget    time.Duration
	Remaining int64
	Length    int
}
type speedServiceEffect struct {
	Kind       string
	Upload     bool
	Count      int
	Bytes      int64
	Budget     time.Duration
	Status     int
	Text, Node string
	Temporary  bool
	Result     speedTestResult
}
type speedServiceReply struct {
	Err            error
	Flag           bool
	Text           string
	Bytes, Started int64
	Elapsed        time.Duration
	Rate           float64
}
type speedServiceResult struct {
	Err       error
	Bytes     int64
	Elapsed   time.Duration
	EOF       bool
	Status    int
	Node      string
	Temporary bool
	Speed     speedTestResult
}
type speedServiceEffects interface {
	Call(speedServiceEffect) speedServiceReply
	Release()
}

type speedResponsePanic struct{ Value any }
