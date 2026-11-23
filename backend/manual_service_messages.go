package main

import "fmt"

func manualDuplicateMessage0(name string) string {
	return fmt.Sprintf("已经有一个名为「%s」的手动节点", name)
}
func manualDuplicateMessage1(name string) string {
	return fmt.Sprintf("订阅里已经有一个名为「%s」的节点，请换个名字", name)
}
func manualErrorMessage0() string  { return "无法保存手动节点" }
func manualErrorMessage1() string  { return "无法写入手动节点文件" }
func manualErrorMessage2() string  { return "没有找到这个手动节点" }
func manualErrorMessage3() string  { return "无法保存手动节点列表" }
func manualErrorMessage4() string  { return "无法更新手动节点文件" }
func manualRunningMessage() string { return "断开连接后再增删手动节点" }
