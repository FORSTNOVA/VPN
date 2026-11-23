package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type chinaOracle struct{ fx chinaServiceEffects }

func (o chinaOracle) call(k string) chinaServiceReply { return o.fx.Call(chinaServiceEffect{Kind: k}) }
func (o chinaOracle) parse(body []byte) ([]string, error) {
	lines := o.fx.Call(chinaServiceEffect{Kind: "lines", Body: body}).Names
	seen := map[string]bool{}
	ranges := make([]string, 0, 8192)
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if n := strings.IndexAny(line, " \t#"); n >= 0 {
			line = line[:n]
		}
		r := o.fx.Call(chinaServiceEffect{Kind: "cidr", Name: line})
		if r.Err != nil {
			return nil, fmt.Errorf("not an address range: %q", line)
		}
		if !seen[r.Text] {
			seen[r.Text] = true
			ranges = append(ranges, r.Text)
		}
	}
	if len(ranges) < chinaIPMinRanges {
		return nil, fmt.Errorf("only %d address ranges, expected at least %d", len(ranges), chinaIPMinRanges)
	}
	sort.Strings(ranges)
	return ranges, nil
}
func (o chinaOracle) download() ([]byte, string, error) {
	src := o.call("sources")
	var last error
	for _, mirror := range src.Names {
		ranges := make([]string, 0, 16384)
		failed := false
		for _, name := range src.Ranges {
			r := o.fx.Call(chinaServiceEffect{Kind: "fetch", Name: mirror + name})
			if r.Err != nil {
				last = r.Err
				failed = true
				break
			}
			part, e := o.parse(r.Body)
			if e != nil {
				last = fmt.Errorf("%s%s: %w", mirror, name, e)
				failed = true
				break
			}
			ranges = append(ranges, part...)
		}
		if failed || len(ranges) == 0 {
			continue
		}
		sort.Strings(ranges)
		return []byte(strings.Join(ranges, "\n") + "\n"), mirror, nil
	}
	if last == nil {
		last = fmt.Errorf("no source for the address list answered")
	}
	return nil, "", last
}
func (o chinaOracle) write(ranges []string) error {
	if r := o.call("mkdir"); r.Err != nil {
		return r.Err
	}
	if r := o.fx.Call(chinaServiceEffect{Kind: "write-temp", Body: []byte(strings.Join(ranges, "\n") + "\n")}); r.Err != nil {
		return r.Err
	}
	return o.call("rename").Err
}
func (o chinaOracle) ensure(mode chinaListMode) chinaIPStatus {
	r := o.call("stat")
	updated := r.Now
	err := r.Err
	var ranges []string
	if err == nil {
		r = o.call("read")
		err = r.Err
		if err == nil {
			ranges, err = o.parse(r.Body)
		}
	}
	cached := err == nil
	intact := func() chinaIPStatus {
		return chinaIPStatus{Available: true, Entries: len(ranges), UpdatedAt: updated.Format(time.RFC3339)}
	}
	if mode == chinaListCached {
		if cached {
			return intact()
		}
		return chinaIPStatus{Error: err.Error()}
	}
	if cached && mode == chinaListIfOverdue && o.fx.Call(chinaServiceEffect{Kind: "since", Time: updated}).Flag {
		return intact()
	}
	override := o.call("fetch-override")
	body, source, fetchErr := override.Body, override.Text, override.Err
	if !override.Flag {
		body, source, fetchErr = o.download()
	}
	if fetchErr != nil {
		if cached {
			s := intact()
			s.Error = chinaRefreshError(fetchErr)
			return s
		}
		return chinaIPStatus{Error: fetchErr.Error()}
	}
	fresh, parseErr := o.parse(body)
	if parseErr != nil {
		if cached {
			return intact()
		}
		return chinaIPStatus{Error: parseErr.Error()}
	}
	if e := o.write(fresh); e != nil {
		if cached {
			s := intact()
			s.Error = chinaWriteError(e)
			return s
		}
		return chinaIPStatus{Error: e.Error()}
	}
	return chinaIPStatus{Available: true, Entries: len(fresh), Source: source, UpdatedAt: o.call("now").Now.Format(time.RFC3339)}
}
func (o chinaOracle) probe() bool {
	if o.call("free-port").Err != nil {
		return false
	}
	if o.call("probe-mkdir").Err != nil {
		return false
	}
	defer o.call("probe-remove-config")
	defer o.call("probe-remove-list")
	if o.call("probe-list-write").Err != nil {
		return false
	}
	if o.call("probe-config-write").Err != nil {
		return false
	}
	r := o.call("probe-check")
	if r.Err != nil {
		o.fx.Call(chinaServiceEffect{Kind: "log", Name: "probe", Text: r.Err.Error()})
		return false
	}
	return true
}
func (o chinaOracle) command(action uint8, status chinaIPStatus, flag bool) {
	o.fx.Call(chinaServiceEffect{Kind: "command", Action: action, Status: status, Flag: flag})
}
func (o chinaOracle) supported() bool {
	s := o.call("state").State
	if s.Probed {
		return s.RuleOK
	}
	o.command(3, chinaIPStatus{}, true)
	ok := o.probe()
	o.command(4, chinaIPStatus{}, ok)
	return ok
}
func runGoChinaService(q chinaServiceRequest, fx chinaServiceEffects) (r chinaServiceResult) {
	defer fx.Release()
	o := chinaOracle{fx}
	switch q.Op {
	case "read":
		v := o.call("stat")
		if v.Err != nil {
			r.Err = v.Err
			return
		}
		raw := o.call("read")
		if raw.Err != nil {
			r.Err = raw.Err
			return
		}
		var ranges []string
		ranges, r.Err = o.parse(raw.Body)
		if r.Err == nil {
			r.Now = v.Now
			r.Status.Entries = len(ranges)
		}
	case "parse":
		r.Ranges, r.Err = o.parse(q.Body)
	case "download":
		r.Body, r.Source, r.Err = o.download()
	case "write":
		r.Err = o.write(q.Ranges)
	case "ensure":
		r.Status = o.ensure(q.Mode)
	case "probe":
		r.Flag = o.probe()
	case "supported":
		r.Flag = o.supported()
	case "status":
		s := o.call("state").State
		r.Status = s.Status
		r.Status.Refreshing = s.Fetching
		r.Status.Active = s.Active
	case "start-refresh":
		o.call("lock")
		s := o.call("state").State
		if s.Fetching {
			o.call("unlock")
			return
		}
		o.command(2, chinaIPStatus{}, true)
		o.call("unlock")
		o.fx.Call(chinaServiceEffect{Kind: "spawn", Index: int(q.Mode)})
		r.Flag = true
	case "refresh":
		o.call("lock")
		o.call("state")
		o.call("unlock")
		r.Status = o.ensure(q.Mode)
		o.call("lock")
		o.command(2, chinaIPStatus{}, false)
		o.command(1, r.Status, false)
		o.call("unlock")
		if r.Status.Source != "" {
			o.fx.Call(chinaServiceEffect{Kind: "log", Name: "source", Status: r.Status})
		} else if !r.Status.Available {
			o.fx.Call(chinaServiceEffect{Kind: "log", Name: "unavailable", Status: r.Status})
		}
	case "options":
		status := o.ensure(chinaListCached)
		o.command(1, status, false)
		b := o.call("options-base")
		r.Options = b.Options
		if status.Available {
			r.Options.ChinaDirect = o.supported()
		}
		o.fx.Call(chinaServiceEffect{Kind: "options-china", Flag: r.Options.ChinaDirect})
		if b.ImportPolicy {
			v := o.call("options-import")
			r.Options = v.Options
			if v.Err != nil {
				r.Err = v.Err
				return
			}
		}
		if r.Options.TUN && r.Options.DomesticWeb && !r.Options.ChinaDirect {
			r.Err = chinaDomesticError()
			return
		}
		v := o.call("options-chain")
		r.Options = v.Options
		if v.Err != nil {
			r.Err = v.Err
			return
		}
		if r.Options.TUN {
			r.Options = o.call("options-domains").Options
		}
	default:
		r.Err = fmt.Errorf("unknown china operation")
	}
	return
}
