package main

import "time"

type kernelAdmissionRequest struct {
	Op, Name string
	Port     int
	Timeout  time.Duration
}
type kernelAdmissionEffect struct {
	Kind, Text string
	Index      int
	Duration   time.Duration
}
type kernelAdmissionReply struct {
	Err         error
	Bool, Other bool
	Text        string
	Count       int
	Names       []string
}
type kernelAdmissionResult struct {
	Err  error
	Bool bool
	Text string
}
type kernelAdmissionEffects interface {
	Call(kernelAdmissionEffect) kernelAdmissionReply
	Release()
}
