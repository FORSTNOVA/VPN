package main

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const nativeMaxArguments = 7

func nativeInvocationAllowed(proc uintptr, count int) bool {
	return proc != 0 && count >= 0 && count <= nativeMaxArguments
}
func nativeSymbolAllowed(name string) bool { return name != "" && !strings.ContainsRune(name, 0) }
func nativeLibraryPathAllowed(path string) bool {
	return path != "" && filepath.IsAbs(path) && !strings.ContainsRune(path, 0) && utf8.ValidString(path)
}
