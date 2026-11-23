//go:build !android || !amd64 || !cgo

package main

import _ "modernc.org/sqlite"

const storeDriverName = "sqlite"
