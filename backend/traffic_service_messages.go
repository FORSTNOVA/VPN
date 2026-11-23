package main

func trafficCollectMessage0() string { return "无法读取连接快照，历史采样暂时中断" }
func trafficCollectMessage1() string {
	return "无法保存流量统计，请检查本地数据库与磁盘空间"
}
func trafficHistoryMessage0() string { return "不支持的分组" }
func trafficHistoryMessage1() string { return "分页参数无效" }
func trafficHistoryMessage2() string { return "历史数据库不可用" }
func trafficHistoryMessage3() string { return "无法读取历史统计" }
func trafficCurrentMessage0() string {
	return "内核未运行：连接与流量监控只在连接期间有意义"
}
func trafficCurrentMessage1() string { return "无法读取内核连接表" }
