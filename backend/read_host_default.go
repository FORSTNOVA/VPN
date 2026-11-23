//go:build !windows && (!android || !cgo)

package main

import "errors"

func startAPIHost(int, string, string, bool) (string, func(), error) {
	return "", nil, errors.New("unsupported platform")
}
