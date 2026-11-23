package main

import (
	"fmt"
	"strings"
)

func chainTrimName(s string) string { return strings.TrimSpace(s) }
func chainCompileMessage0(args ...any) string {
	return "请选择 2 至 8 个不同的节点"
}
func chainCompileMessage1(args ...any) string {
	return "无法读取订阅节点"
}
func chainCompileMessage2(args ...any) string {
	return "请选择 1 至 8 个节点"
}
func chainCompileMessage3(args ...any) string {
	return fmt.Sprintf("第 %d 跳重复选择了同一个节点", args...)
}
func chainCompileMessage4(args ...any) string {
	return fmt.Sprintf("第 %d 跳节点缺失或重名，请重新选择", args...)
}
func chainCompileMessage5(args ...any) string {
	return fmt.Sprintf("第 %d 跳已有前置代理，请选择独立节点", args...)
}
func chainCompileMessage6(args ...any) string {
	return fmt.Sprintf("第 %d 跳缺少代理服务器配置", args...)
}
func chainCompileMessage7(args ...any) string {
	return fmt.Sprintf("节点名称与链路保留名称 %s 冲突", args...)
}
func chainCompileMessage8(args ...any) string {
	return "链路出口未验证为已锁定地区，请先解除地区锁或选择已验证的同地区出口"
}
func chainHTTPMessage0() string { return "无效的链式代理设置" }
func chainHTTPMessage1() string {
	return "请先断开连接并结束地区检测，再修改链式代理"
}
func chainHTTPMessage2() string { return "代理链已修改，请重新选择" }
func chainHTTPMessage3() string { return "未能保存链式代理" }
func chainHTTPMessage4() string { return "无效的代理链" }
func chainHTTPMessage5() string { return "名称须为 1 至 80 个字符" }
func chainHTTPMessage6() string { return "原代理链已不存在" }
func chainHTTPMessage7() string { return "代理链名称已存在" }
func chainHTTPMessage8() string {
	return "请先断开连接并结束地区检测，再修改当前使用的代理链"
}
func chainHTTPMessage9() string { return "保存失败" }
