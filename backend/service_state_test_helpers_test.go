package main

func testTrafficBaseline(a *app) trafficSample {
	s := a.serviceStateLocked()
	v, e := s.read()
	if e != nil {
		panic(e)
	}
	return trafficSample{at: s.clocks[v.TimeToken], up: v.Up, down: v.Down}
}
func testTrafficError(a *app) string {
	v, e := a.serviceStateLocked().read()
	if e != nil {
		panic(e)
	}
	return v.SamplingError
}
func testSeedTraffic(a *app, v trafficSample, msg string) *app {
	if e := a.serviceStateLocked().baseline(v); e != nil {
		panic(e)
	}
	if e := a.serviceStateLocked().samplingError(msg); e != nil {
		panic(e)
	}
	return a
}
