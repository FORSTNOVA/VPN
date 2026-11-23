//go:build windows

package main

// setSystemProxy turns the machine's own proxy setting on or off, through the
// helper script that owns the backup and restore of what was there before.
func (a *app) setSystemProxy(enable bool) error {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "proxy", Enable: enable})
	e.a = a
	e.home = a.home
	return runBootstrapWithEffects(e.q, e).Err
}

// restoreProxy puts the setting back, and only bothers when this run is what
// turned it on: a backup left behind by a run that crashed is recovered at
// startup instead, and touching it twice would restore the wrong thing.
func (a *app) restoreProxy() {
	if a.proxyOn && a.setSystemProxy(false) == nil {
		a.proxyOn = false
	}
}
