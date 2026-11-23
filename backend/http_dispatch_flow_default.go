//go:build !rustcore || (!windows && !android) || (windows && !amd64) || (android && (!(arm64 || amd64) || !cgo))

package main

func runSelectedHTTPDispatch(q httpDispatchRequest) httpDispatchPlan { return runGoHTTPDispatch(q) }

func readHostSupported() bool { return false }

func writeHostSupported() bool { return false }

func windowsKernelHostEnabled() bool { return false }
