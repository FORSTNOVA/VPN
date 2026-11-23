//go:build android && (!rustcore || !cgo || (!amd64 && !arm64))

package main

func (*androidTunLease) applyRust(int, int32, bool) (int, bool) {
	return 0, false
}
