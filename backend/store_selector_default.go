//go:build !((windows && amd64 && (ruststore || rustcore)) || (android && cgo && rustcore && (amd64 || arm64)))

package main

func selectStoreBackend(path string) (storeBackend, error) { return openGoStoreBackend(path) }
