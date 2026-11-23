package main

import "errors"

func chinaRefreshError(e error) string {
	return "刷新失败，继续使用已有的列表：" + e.Error()
}
func chinaWriteError(e error) string {
	return "写入失败，继续使用已有的列表：" + e.Error()
}
func chinaDomesticError() error {
	return errors.New("国内加速需要可用的国内直连列表，请获取或更新列表后重试")
}
func chinaImportError() error { return errors.New("无法解析订阅规则") }
