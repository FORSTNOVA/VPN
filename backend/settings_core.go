package main

func validSocksSettingGo(port int, windows bool) bool { return windows && port >= 0 && port <= 65535 }
