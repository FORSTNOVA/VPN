//go:build android

package main

// Android has no machine-wide proxy setting to write, and that is the whole
// difference between the two platforms: on Windows this service has to take a
// setting away from every other program and put it back afterwards, while on
// Android the tunnel is a VpnService the user authorised once. The calls below
// exist so that the code above them does not have to know which one it is
// running on; none of them can fail, because none of them does anything.

func (a *app) setSystemProxy(bool) error { return nil }

func (a *app) restoreProxy() { a.proxyOn = false }

// readWindowsProxyState answers the question the connection page asks on
// Windows — is the machine's proxy setting on, and where does it point. On
// Android the equivalent fact is whether the VpnService holds the tunnel, which
// the state route reports separately.
func readWindowsProxyState() (bool, string, error) { return false, "", nil }

// proxyServedByAnotherInstance exists because Windows has one proxy setting for
// the whole machine and two copies of this app can both reach for it. An
// Android app is one process under one user id with one VpnService, and the
// system refuses a second VpnService from the same app, so there is nothing to
// arbitrate.
func (a *app) proxyServedByAnotherInstance() bool { return false }
