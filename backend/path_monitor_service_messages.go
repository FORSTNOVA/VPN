package main

func pathMonitorMessage(i int) string {
	return []string{"网址无效：%s（只支持 http/https）", "名称最多 40 个字", "最多 %d 个检测目标（含 %d 个内置项）", "连接后再检测通路", "自动恢复检测已取消；Google 和 YouTube 仍按间隔检查。"}[i]
}
