package main

// proxyOwner is what the copy of this app that is serving the machine's proxy
// setting publishes about itself.
//
// The record exists because Windows has one proxy setting for the whole
// machine and more than one copy of this app can reach for it: a portable copy
// beside a program folder and an installed one in the profile are two services
// that never see each other's files, but both can write that one setting. The
// record names the profile directory that owns it and the ports that copy
// listens on. The service's own port is in it because that is the port that can
// answer "is a SmartVPN still there?", the proxy port being the kernel's.
//
// The type is shared with the platform that has no such record so that the
// service structure does not have to be built twice; on Android nothing ever
// fills one in, and proxy_android.go says why.
type proxyOwner struct {
	Home      string `json:"home"`
	APIPort   int    `json:"apiPort"`
	MixedPort int    `json:"mixedPort"`
}
