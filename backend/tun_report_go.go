package main

import (
	"fmt"
	"strings"
)

func (a *tunReportOracle) windowsStatus() map[string]any {
	a.call("lock", "")
	reason := a.unavailable(false)
	return map[string]any{
		"mode":      a.call("mode", "").Text,
		"elevated":  a.call("elevated", "").Flag,
		"wintun":    a.wintun(),
		"interface": tunReportInterface,
		"stack":     tunStack,
		"mtu":       tunMTU,
		"available": reason == nil,
		"reason":    tunReason(reason),
		"active":    a.call("running", "").Flag && a.call("mode", "").Text == modeTUN,
	}
}

func (a *tunReportOracle) windowsChecks() map[string]any {
	a.call("lock", "")

	checks := make([]tunCheck, 0, 8)
	if a.call("elevated", "").Flag {
		checks = append(checks, tunCheck{Name: "管理员权限", State: "ok", Detail: "进程已具备管理员权限"})
	} else {
		checks = append(checks, tunCheck{Name: "管理员权限", State: "fail", Detail: tunReportNoAdmin})
	}
	status := a.wintun()
	if status.Installed {
		checks = append(checks, tunCheck{
			Name: "wintun.dll", State: "ok", Value: status.Path,
			Detail: fmt.Sprintf("%s · sha256 %s…", tunReportWintunVersion, shortDigest(status.SHA256)),
		})
	} else {
		checks = append(checks, tunCheck{Name: "wintun.dll", State: "fail", Value: status.Path, Detail: tunReportNoWintun})
	}

	active := a.call("running", "").Flag && a.call("mode", "").Text == modeTUN
	if !active {
		checks = append(checks, tunCheck{Name: "TUN 连接", State: "warn",
			Detail: "当前不是 TUN 模式或尚未连接，路径检查只在 TUN 连接后才有意义"})
		return map[string]any{"active": false, "checks": checks}
	}

	report, err := a.adapter()
	if err != nil {
		checks = append(checks, tunCheck{Name: "TUN 网卡", State: "fail", Detail: err.Error()})
	} else {
		if report.Present && report.Status == "Up" {
			checks = append(checks, tunCheck{Name: "TUN 网卡", State: "ok", Value: report.Name, Detail: "虚拟网卡存在且已启用"})
		} else {
			checks = append(checks, tunCheck{Name: "TUN 网卡", State: "fail", Value: report.Name,
				Detail: fmt.Sprintf("状态为 %s，虚拟网卡未正常工作", a.call("quote", report.Status).Text)})
		}
		switch {
		case report.IPv4Route && report.IPv6Route:
			checks = append(checks, tunCheck{Name: "默认路由", State: "ok", Value: strings.Join(report.RouteVia, " "),
				Detail: "IPv4 与 IPv6 默认路由都指向虚拟网卡"})
		case report.IPv4Route || report.IPv6Route:
			checks = append(checks, tunCheck{Name: "默认路由", State: "warn", Value: strings.Join(report.RouteVia, " "),
				Detail: "只有一种协议的默认路由经过虚拟网卡，另一端可能绕过隧道"})
		default:
			checks = append(checks, tunCheck{Name: "默认路由", State: "fail",
				Detail: "默认路由没有指向虚拟网卡，流量不会进入隧道"})
		}
	}

	checks = append(checks, a.dnsHijack())
	checks = append(checks, a.nodeDomain())

	checks = append(checks, a.exitPath("IPv4 出口"))
	// There is no IPv6 exit to compare against because the resolver hands out no
	// AAAA records on purpose: a node with an IPv4-only exit would otherwise be
	// asked to carry every connection, which is what stalls the whole system.
	checks = append(checks, tunCheck{Name: "IPv6 出口", State: "warn", Detail: tunIPv6Disabled})

	return map[string]any{"active": true, "checks": checks, "interface": tunReportInterface}
}

func (a *tunReportOracle) androidStatus() map[string]any {
	a.call("refresh", "")
	a.call("lock", "")
	authorized, fd, iface, routes, dns := a.tunnelState()
	reason := a.unavailable(true)
	return map[string]any{
		"mode":       a.call("mode", "").Text,
		"elevated":   authorized,
		"interface":  iface,
		"stack":      tunStack,
		"mtu":        tunMTU,
		"routes":     routes,
		"dns":        dns,
		"available":  reason == nil,
		"reason":     tunReason(reason),
		"active":     a.call("running", "").Flag && fd > 0,
		"descriptor": fd,
	}
}

func (a *tunReportOracle) androidChecks() map[string]any {
	a.call("refresh", "")
	a.call("lock", "")

	authorized, fd, iface, routes, dns := a.tunnelState()
	checks := make([]tunCheck, 0, 8)
	if authorized {
		checks = append(checks, tunCheck{Name: "VPN 授权", State: "ok", Detail: "系统已允许 SmartVPN 建立 VPN 连接"})
	} else {
		checks = append(checks, tunCheck{Name: "VPN 授权", State: "fail", Detail: "尚未授权，连接会由系统弹窗询问"})
	}
	if fd > 0 {
		state, detail := "ok", fmt.Sprintf("VpnService 已建立隧道（fd %d）", fd)
		if !a.call("descriptor-alive", "").Flag {
			// Worth failing loudly: in this state the device has no network at
			// all, and everything else on this page would look healthy.
			state = "fail"
			detail = fmt.Sprintf("隧道描述符 %d 已被提前关闭：隧道还在接管路由，但没人再读它，"+
				"此时全部应用的流量都会被丢弃", fd)
		}
		checks = append(checks, tunCheck{Name: "隧道", State: state, Value: iface, Detail: detail})
	} else {
		checks = append(checks, tunCheck{Name: "隧道", State: "fail", Detail: "VpnService 尚未建立隧道"})
	}
	switch {
	case len(routes) == 0:
		checks = append(checks, tunCheck{Name: "默认路由", State: "warn",
			Detail: "读不到隧道的路由表，无法确认默认流量是否进入隧道"})
	case hasDefaultRoute(routes):
		checks = append(checks, tunCheck{Name: "默认路由", State: "ok", Value: strings.Join(routes, " "),
			Detail: "VpnService 把默认路由指向了隧道，全部应用的流量都经过这里"})
	default:
		checks = append(checks, tunCheck{Name: "默认路由", State: "fail", Value: strings.Join(routes, " "),
			Detail: "隧道没有默认路由，受保护流量会走系统网络"})
	}
	if len(dns) > 0 {
		checks = append(checks, tunCheck{Name: "隧道 DNS", State: "ok", Value: strings.Join(dns, ", "),
			Detail: "系统解析地址落在隧道内，查询会由内核接管"})
	}

	active := a.call("running", "").Flag && fd > 0
	if !active {
		checks = append(checks, tunCheck{Name: "连接", State: "warn",
			Detail: "尚未连接，路径检查只在连接后才有意义"})
		return map[string]any{"active": false, "checks": checks}
	}

	checks = append(checks, dnsHijackNote())
	checks = append(checks, a.nodeDomain())
	checks = append(checks, a.exitRegion())
	checks = append(checks, tunCheck{Name: "IPv6 出口", State: "warn", Detail: tunIPv6Disabled})

	return map[string]any{"active": true, "checks": checks, "interface": iface}
}

func (a *tunReportOracle) dnsHijack() tunCheck {
	check := tunCheck{Name: "DNS 接管（fake-ip）", State: "fail"}
	addresses, err := a.lookup("")
	if err != nil || len(addresses) == 0 {
		check.Detail = "域名解析失败，无法确认 DNS 是否经过内核"
		return check
	}
	check.Value = strings.Join(addresses, ", ")
	for _, address := range addresses {
		if a.call("fake-address", address).Flag {
			check.State = "ok"
			check.Detail = "解析结果落在 fake-ip 网段，DNS 查询已由内核接管"
			return check
		}
	}
	check.Detail = "解析结果不是 fake-ip，DNS 可能绕过内核（存在 DNS 泄漏风险）"
	return check
}

func (a *tunReportOracle) nodeDomain() tunCheck {
	check := tunCheck{Name: "节点域名解析", State: "warn"}
	body, err := a.readProfile()
	if err != nil {
		check.Detail = "读不到订阅文件，无法确认节点域名"
		return check
	}
	// A node's own hostname rather than the suffix its hostnames share: what the
	// kernel has to resolve is the host in the profile, and a suffix like
	// `+.qos.onl` is not a name that answers anywhere.
	host := a.hostname(body)
	if host == "" {
		check.State = "ok"
		check.Detail = "订阅里的节点使用 IP 直连，没有需要排除的域名"
		return check
	}
	check.Value = host
	addresses, err := a.lookup(host)
	if err != nil || len(addresses) == 0 {
		check.State = "fail"
		check.Detail = "节点域名无法解析，节点会因此不可达"
		return check
	}
	check.Value = host + " → " + strings.Join(addresses, ", ")
	for _, address := range addresses {
		if a.call("fake-address", address).Flag {
			check.State = "fail"
			check.Detail = "节点域名被 fake-ip 接管：代理自身到节点的连接会被吸回隧道"
			return check
		}
	}
	check.State = "ok"
	check.Detail = "节点域名解析为真实地址，代理自身的连接不会进入隧道"
	return check
}

func (a *tunReportOracle) exitRegion() tunCheck {
	check := tunCheck{Name: "出口地区", State: "warn"}
	node := a.call("active-node", "").Text
	record, known := a.region(node)
	if !known || record.ExitIP == "" {
		check.Detail = "当前节点还没有验证过的出口地址，先在地区页做一次实测"
		return check
	}

	measured := a.call("exit-tunnel", "").Exit
	if measured.Address == "" {
		check.State = "fail"
		check.Detail = "经隧道查询出口地址失败：" + measured.Error
		return check
	}
	check.Value = measured.Address + " · " + record.Country
	if measured.Address != record.ExitIP {
		check.State = "fail"
		check.Detail = fmt.Sprintf("出口地址与验证过的 %s 不一致（当时为 %s），节点可能已经换了出口",
			record.ExitIP, record.Country)
		return check
	}
	if a.call("locked-region", "").Text != "" && !a.call("locked-region", "").Flag {
		check.State = "fail"
		check.Detail = fmt.Sprintf("锁定地区是 %s，当前出口却是 %s", a.call("locked-region", "").Text, record.Country)
		return check
	}
	check.State = "ok"
	if a.call("locked-region", "").Text == "" {
		check.Detail = "出口地址与验证结果一致；没有锁定地区，因此只是报告"
		return check
	}
	check.Detail = "出口地址与锁定地区的验证结果一致，锁定生效"
	return check
}

func (a *tunReportOracle) exitPath(name string) tunCheck {
	check := tunCheck{Name: name}
	tunnelled := a.call("exit-tunnel", "").Exit
	system := a.call("exit-system", "").Exit
	switch {
	case tunnelled.Address == "" && system.Address == "":
		check.State = "warn"
		check.Detail = fmt.Sprintf("两条路径都未能取得地址（%s / %s）", tunnelled.Error, system.Error)
	case system.Address == "":
		check.State = "warn"
		check.Value = tunnelled.Address
		check.Detail = "系统路径未响应：" + system.Error
	case tunnelled.Address == "":
		check.State = "fail"
		check.Value = system.Address
		check.Detail = "本机代理路径不可达，无法与系统路径比较"
	case system.Address == tunnelled.Address:
		check.State = "ok"
		check.Value = system.Address
		check.Detail = "系统路径与隧道出口一致，流量确实经过内核"
	default:
		check.State = "fail"
		check.Value = fmt.Sprintf("系统 %s / 隧道 %s", system.Address, tunnelled.Address)
		check.Detail = "系统路径的出口与隧道不一致，受保护流量可能绕过隧道"
	}
	return check
}

func dnsHijackNote() tunCheck {
	return tunCheck{
		Name:  "DNS 接管",
		State: "warn",
		Detail: "本机进程有意留在隧道之外（订阅与节点连接必须直连），因此无法从进程内验证" +
			"系统 DNS 是否被内核接管；默认路由与出口地区两项才是这台设备生效的证据",
	}
}

func hasDefaultRoute(routes []string) bool {
	for _, route := range routes {
		if route == "0.0.0.0/0" || route == "::/0" {
			return true
		}
	}
	return false
}
