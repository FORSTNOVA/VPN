//go:build !windows

package main

func probeInterfaceDNS(string) ([]string, error) { return nil, nil }
