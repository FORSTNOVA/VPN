package main

func testHealthApp(a *app, edit func(*healthDomainProjection)) *app {
	testEditHealth(a, edit)
	return a
}
