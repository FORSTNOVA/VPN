package main

import (
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Facts are codec outputs, not selected UI values. All original byte strings,
// including malformed UTF-8, remain byte-preserving across the pure ABI.
type trafficProjectionFacts struct {
	Process, Path, Base, Host, IP, Port, HostPort, IPPort, UID string
	Network, Type, Rule, Payload                               string
	Chains                                                     []string
	Upload, Download, Elapsed                                  int64
	Parsed                                                     bool
}
type trafficProjectionRequest struct {
	Op                      byte
	Rows                    []trafficProjectionFacts
	Period, Date, Formatted string
	Parsed                  bool
	Offset, Limit           string
	OffsetValue, LimitValue int64
	OffsetOK, LimitOK       bool
}
type trafficProjectionResult struct {
	Rows          []connectionRow
	Text, Other   string
	Code          int
	Offset, Limit int
}

const (
	trafficProjectionRows byte = iota
	trafficProjectionProcess
	trafficProjectionTarget
	trafficProjectionRule
	trafficProjectionChain
	trafficProjectionApp
	trafficProjectionDatePlan
	trafficProjectionDateResult
	trafficProjectionPagination
)

func trafficProjectionMetadata(m kernelMetadata) trafficProjectionFacts {
	f := trafficProjectionFacts{Process: strings.TrimSpace(m.Process), Path: strings.TrimSpace(m.ProcessPath), Host: strings.TrimSpace(m.Host), IP: strings.TrimSpace(m.DestinationIP), Port: strings.TrimSpace(string(m.DestinationPort)), UID: string(m.UID), Network: m.Network, Type: m.Type}
	f.Base = path.Base(strings.ReplaceAll(f.Path, `\`, "/"))
	f.HostPort = net.JoinHostPort(f.Host, f.Port)
	f.IPPort = net.JoinHostPort(f.IP, f.Port)
	return f
}
func trafficProjectionConnection(c kernelConnection, now time.Time) trafficProjectionFacts {
	f := trafficProjectionMetadata(c.Metadata)
	f.Rule = strings.TrimSpace(c.Rule)
	f.Payload = strings.TrimSpace(c.RulePayload)
	f.Upload = c.Upload
	f.Download = c.Download
	for _, s := range c.Chains {
		f.Chains = append(f.Chains, strings.TrimSpace(s))
	}
	start, err := time.Parse(time.RFC3339Nano, c.Start)
	f.Parsed = err == nil
	if err == nil {
		f.Elapsed = int64(now.Sub(start))
	}
	return f
}
func trafficProjectionProcessGo(f trafficProjectionFacts) string {
	if f.Process != "" {
		return f.Process
	}
	if f.Path == "" || f.Base == "." || f.Base == "/" {
		return ""
	}
	return f.Base
}
func trafficProjectionTargetGo(f trafficProjectionFacts) string {
	if f.Host != "" {
		if f.Port != "" {
			return f.HostPort
		}
		return f.Host
	}
	if f.IP == "" {
		return ""
	}
	if f.Port != "" {
		return f.IPPort
	}
	return f.IP
}
func trafficProjectionRuleGo(f trafficProjectionFacts) string {
	if f.Rule == "" {
		return ""
	}
	if f.Payload == "" {
		return f.Rule
	}
	return f.Rule + "(" + f.Payload + ")"
}
func trafficProjectionChainGo(f trafficProjectionFacts) string {
	for _, s := range f.Chains {
		if s != "" {
			return s
		}
	}
	return ""
}
func runGoTrafficProjection(q trafficProjectionRequest) (r trafficProjectionResult) {
	var f trafficProjectionFacts
	if len(q.Rows) > 0 {
		f = q.Rows[0]
	}
	switch q.Op {
	case trafficProjectionRows:
		r.Rows = make([]connectionRow, 0, len(q.Rows))
		for _, f := range q.Rows {
			row := connectionRow{Process: trafficProjectionProcessGo(f), Target: trafficProjectionTargetGo(f), Network: f.Network, Type: f.Type, Rule: trafficProjectionRuleGo(f), Chain: trafficProjectionChainGo(f), Upload: f.Upload, Download: f.Download}
			if f.Parsed && f.Elapsed > 0 {
				row.DurationMs = f.Elapsed / int64(time.Millisecond)
			}
			r.Rows = append(r.Rows, row)
		}
		sort.SliceStable(r.Rows, func(i, j int) bool { return r.Rows[i].Upload+r.Rows[i].Download > r.Rows[j].Upload+r.Rows[j].Download })
		if len(r.Rows) > maxConnectionRows {
			r.Rows = r.Rows[:maxConnectionRows]
		}
	case trafficProjectionProcess:
		r.Text = trafficProjectionProcessGo(f)
	case trafficProjectionTarget:
		r.Text = trafficProjectionTargetGo(f)
	case trafficProjectionRule:
		r.Text = trafficProjectionRuleGo(f)
	case trafficProjectionChain:
		r.Text = trafficProjectionChainGo(f)
	case trafficProjectionApp:
		name := trafficProjectionProcessGo(f)
		r.Other = name
		if f.Path != "" {
			r.Text = "path:" + f.Path
		} else if name != "" {
			r.Text = "process:" + name
		} else if f.UID != "" && f.UID != "-1" {
			r.Text = "uid:" + f.UID
			r.Other = "UID " + f.UID
		} else {
			r.Text = "unknown"
			r.Other = trafficProjectionUnknownApp()
		}
	case trafficProjectionDatePlan:
		switch q.Period {
		case "hour":
			r.Text = "2006-01-02T15"
		case "day":
			r.Text = "2006-01-02"
		case "month":
			r.Text = "2006-01"
		case "year":
			r.Text = "2006"
		default:
			r.Code = 3
			return
		}
		r.Code = 2
		if q.Date == "" {
			r.Code = 1
		}
	case trafficProjectionDateResult:
		if !q.Parsed || q.Formatted != q.Date {
			r.Code = 4
		} else {
			r.Text = q.Date
		}
	case trafficProjectionPagination:
		r.Limit = 100
		if q.Offset != "" {
			if !q.OffsetOK || q.OffsetValue < 0 {
				r.Code = 5
				return trafficProjectionResult{Code: 5}
			}
			r.Offset = int(q.OffsetValue)
		}
		if q.Limit != "" {
			if !q.LimitOK || q.LimitValue < 1 || q.LimitValue > 200 {
				return trafficProjectionResult{Code: 5}
			}
			r.Limit = int(q.LimitValue)
		}
	default:
		r.Code = 6
	}
	return
}
func projectionHistoryDate(period, date string, now time.Time) (string, error) {
	p := runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionDatePlan, Period: period, Date: date})
	switch p.Code {
	case 3:
		return "", trafficProjectionDateError(false)
	case 1:
		return now.Format(p.Text), nil
	case 2:
		parsed, err := time.ParseInLocation(p.Text, date, now.Location())
		formatted := ""
		if err == nil {
			formatted = parsed.Format(p.Text)
		}
		r := runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionDateResult, Date: date, Parsed: err == nil, Formatted: formatted})
		if r.Code != 0 {
			return "", trafficProjectionDateError(true)
		}
		return r.Text, nil
	default:
		panic("invalid traffic date plan")
	}
}
func projectionPagination(offset, limit string) (int, int, error) {
	o, oe := strconv.Atoi(offset)
	l, le := strconv.Atoi(limit)
	r := runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionPagination, Offset: offset, Limit: limit, OffsetValue: int64(o), LimitValue: int64(l), OffsetOK: oe == nil, LimitOK: le == nil})
	if r.Code != 0 {
		return 0, 0, trafficProjectionPaginationError()
	}
	return r.Offset, r.Limit, nil
}
