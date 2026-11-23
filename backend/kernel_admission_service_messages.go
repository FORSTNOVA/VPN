package main

import (
	"errors"
	"fmt"
)

func kernelAdmissionError(code int, text string, port int, cause error) error {
	switch code {
	case 1:
		return fmt.Errorf("没找到 Mihomo 内核：%s（把它放在这个位置，或在「订阅与内核」里填写路径）", text)
	case 2:
		return errors.New("could not open Mihomo log")
	case 3:
		return errors.New("could not start Mihomo")
	case 4:
		return fmt.Errorf("无法核对 SOCKS5 端口 %d 的监听进程：%w", port, cause)
	case 5:
		return fmt.Errorf("内核未能监听 SOCKS5 端口 %s，请检查端口占用后重连", text)
	case 6:
		return errors.New("防泄露连接失败：SmartVPN 虚拟网卡未就绪")
	case 7:
		return errors.New("防泄露连接失败：IPv4 出口未进入 SmartVPN 网卡")
	case 8:
		return errors.New("防泄露连接失败：无法确认 IPv6 出口由 SmartVPN 接管")
	case 9:
		return fmt.Errorf("%v: %s", cause, text)
	}
	return errors.New("unknown admission error")
}
