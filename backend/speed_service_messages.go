package main

import "errors"

func speedEmptyError(up bool) error {
	if up {
		return errors.New("测速没有发出任何数据（节点或链路不可用）")
	}
	return errors.New("测速没有收到任何数据（节点或链路不可用）")
}
func speedSelectionError() error {
	return errors.New("select a subscription node before measuring speed")
}
