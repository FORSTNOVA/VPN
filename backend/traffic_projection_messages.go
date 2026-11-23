package main

import "errors"

func trafficProjectionUnknownApp() string { return "未识别应用" }
func trafficProjectionDateError(date bool) error {
	if date {
		return errors.New("日期格式与统计周期不符")
	}
	return errors.New("统计周期必须为 hour、day、month 或 year")
}
func trafficProjectionPaginationError() error { return errors.New("invalid pagination") }
