//go:build android

package main

func (e *appTunReportEffects) platform(q tunReportEffect) (r tunReportReply) {
	switch q.Kind {
	case "refresh":
		tunnel.refresh()
	case "tunnel-state":
		r.Flag, r.Number, r.Text, r.Names, r.Other = tunnel.state()
	case "descriptor-alive":
		r.Flag = tunnel.descriptorAlive()
	default:
		panic("unsupported android tun effect: " + q.Kind)
	}
	return
}
