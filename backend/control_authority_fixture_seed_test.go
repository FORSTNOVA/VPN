package main

// Test-only seeds use the same typed commands as production. Chain resources
// retain their original map/slice references and are not serialized.
func testControlApp(a *app, edit func(*app)) *app { edit(a); return a }
func testSetSettings(a *app, s settings) {
	before := a.settingsLocked()
	a.applySettingsControlDiffLocked(before, s)
	a.settings.Chain = s.Chain
	a.settings.Chains = s.Chains
}
func testEditSettings(a *app, edit func(*settings)) {
	before := a.settingsLocked()
	next := before
	edit(&next)
	a.applySettingsControlDiffLocked(before, next)
	a.settings.Chain = next.Chain
	a.settings.Chains = next.Chains
}
