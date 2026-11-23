//go:build android && amd64 && cgo

package main

// Build SQLite with the Android NDK so its filesystem calls use Bionic.
// The Linux amd64 modernc libc issues legacy lstat directly on Android.
import _ "github.com/mattn/go-sqlite3"

const storeDriverName = "sqlite3"
