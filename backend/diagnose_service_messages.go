package main

func diagnoseDNSParseMessage() string    { return "解析器归属服务没有返回结果" }
func diagnoseNoDetailsMessage() string   { return "没有可用的出口信息源" }
func diagnoseExitFailureMessage() string { return "隧道出口不通（请求没有到达）" }
func diagnoseSkippedMessage() string     { return "出口不通，未检测" }
func diagnoseSTUNBuildMessage() string   { return "无法构造 STUN 请求" }
func diagnoseSTUNEmptyMessage() string   { return "没有可用的 STUN 服务器" }
func diagnoseQualityUnknown() string     { return "未知" }
func diagnoseQualityResidential() string { return "原生住宅 (Residential / ISP)" }
func diagnoseQualityHosting() string     { return "机房/数据中心 (Data Center / Hosting)" }
func diagnoseQualityLow() string         { return "低风险 (良好)" }
func diagnoseQualityHigh() string        { return "高风险 (已标记)" }
func diagnoseQualityMedium() string      { return "中风险 (机房/代理)" }
