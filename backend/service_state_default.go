//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

func newSelectedServiceStateBackend() (serviceStateBackend, error) {
	return newGoServiceStateBackend(), nil
}
