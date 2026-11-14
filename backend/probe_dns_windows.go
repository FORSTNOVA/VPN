package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"net"
	"unsafe"
)

// Read the selected adapter's current DNS servers, including DHCP changes.
func probeInterfaceDNS(name string) ([]string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, errors.New("测速网卡不存在")
	}
	size := uint32(15000)
	for {
		buf := make([]byte, size)
		head := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err = windows.GetAdaptersAddresses(windows.AF_UNSPEC, 0, 0, head, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW && size > uint32(len(buf)) {
			continue
		}
		if err != nil {
			return nil, errors.New("无法读取测速网卡 DNS")
		}
		for a := head; a != nil; a = a.Next {
			if int(a.IfIndex) != iface.Index && int(a.Ipv6IfIndex) != iface.Index {
				continue
			}
			var servers []string
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				ip := d.Address.IP()
				if ip == nil || ip.IsUnspecified() {
					continue
				}
				addr := ip.String()
				if ip.To4() == nil && ip.IsLinkLocalUnicast() {
					addr += "%" + iface.Name
				}
				servers = append(servers, addr)
			}
			if len(servers) > 0 {
				return servers, nil
			}
		}
		return nil, errors.New("测速网卡没有可用 DNS")
	}
}
