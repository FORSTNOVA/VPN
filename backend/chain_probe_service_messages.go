package main

import "fmt"

func chainProbeError(i int) string {
	return []string{"无法从入口 DNS 获取 ECH 配置", "内核无法加载该节点配置", "未选择节点", "请求超时", "连接或请求失败", "服务器未复用首次连接"}[i]
}
func chainProbeHTTP(i int) string {
	return []string{"无效节点列表", "测试记录数据库未打开", "未能保存测试记录", "测试记录数据库未打开", "读取测试记录失败", "测速记录格式错误"}[i]
}
func chainProbeStage(i int) string {
	return []string{"加载节点配置", "入口 DNS 配置", "入口 ECH 解析", "入口解析或拨号", "入口拨号（尝试地址）", "入口 DNS 解析", "代理协议握手或链路转发", "入口 UDP 建连（尝试地址）", "建立代理连接", "目标 TLS 握手", "发送 HTTPS 请求", "等待目标响应头", "校验 HTTP 状态", "连接复用"}[i]
}
func chainProbeStatusError(code int) string { return fmt.Sprintf("HTTP %d，预期 204", code) }
