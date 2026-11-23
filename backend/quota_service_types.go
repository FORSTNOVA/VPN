package main

import (
	"net/http"
	"strings"
	"time"
)

type quotaServiceRequest struct {
	Op, ID, OriginalURL, Message string
	Status                       int
}
type quotaServiceResult struct {
	Status   int
	Message  string
	Entry    subscriptionEntry
	HasEntry bool
}
type quotaEffect struct {
	Kind                                 string
	Index                                int
	ID, OriginalURL, URL, Proxy, Message string
	Info                                 *subscriptionUserInfo
	Timestamp                            int64
	Success                              bool
}
type quotaReply struct {
	Entries   []subscriptionEntry
	Strings   []string
	Info      *subscriptionUserInfo
	Entry     subscriptionEntry
	Err       error
	Timestamp int64
}
type quotaServiceEffects interface {
	Call(quotaEffect) quotaReply
	Release()
}

// Each invocation owns its lock lifetime. No initial application snapshot is ever applied.
type appQuotaEffects struct {
	a            *app
	owned        bool
	catalogIndex int
	fetch        func(string, string) (*subscriptionUserInfo, error)
	save         func() error
	proxies      func() []string
	now          func() int64
}

func newAppQuotaEffects(a *app) *appQuotaEffects {
	return &appQuotaEffects{a: a, fetch: fetchSubscriptionUserInfo, save: a.saveSubscriptionsLocked, proxies: a.proxyAddressesForDownloadLocked, now: func() int64 { return time.Now().Unix() }}
}
func (e *appQuotaEffects) Release() {
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appQuotaEffects) Call(q quotaEffect) (r quotaReply) {
	switch q.Kind {
	case "lock":
		if e.owned {
			panic("quota lock already owned")
		}
		e.a.mu.Lock()
		e.owned = true
		found := e.a.catalogQueryLocked(serviceCatalogQuery{Kind: 3, ID: q.ID})
		e.catalogIndex = -1
		if found.Found {
			e.catalogIndex = found.Count
			r.Entries = []subscriptionEntry{e.a.serviceStateLocked().catalogProjection(found.Entry, true)}
		}
	case "unlock":
		if !e.owned {
			panic("quota unlock without lock")
		}
		e.Release()
	case "categorize":
		if !e.owned {
			panic("quota categorize outside lock")
		}
		r.Strings, _ = categorizeSubscriptionInput(q.URL)
	case "proxies":
		if !e.owned {
			panic("quota proxies outside lock")
		}
		r.Strings = e.proxies()
	case "candidates":
		if e.owned {
			panic("quota candidates while locked")
		}
		r.Strings = []string{q.URL}
		if alt, ok := clashFormatURL(q.URL); ok {
			r.Strings = append(r.Strings, alt)
		}
	case "fetch":
		if e.owned {
			panic("quota fetch while locked")
		}
		r.Info, r.Err = e.fetch(q.URL, q.Proxy)
	case "now":
		if !e.owned {
			panic("quota clock outside lock")
		}
		r.Timestamp = e.now()
	case "commit":
		if !e.owned {
			panic("quota commit outside lock")
		}
		if q.Index != 0 || e.catalogIndex < 0 {
			panic("quota commit index")
		}
		v := serviceCatalogEntry{QuotaError: q.Message}
		mask := uint64(catalogQuotaError)
		if q.Success {
			if q.Info == nil {
				panic("quota success without info")
			}
			v.InfoHandle = e.a.serviceStateLocked().registerFetchedQuota(q.Info)
			v.QuotaUpdatedAt = q.Timestamp
			v.QuotaError = ""
			mask |= catalogInfoHandle | catalogQuotaUpdatedAt
		}
		e.a.catalogCommandLocked(serviceCatalogCommand{Action: catalogQuotaGuarded, Index: e.catalogIndex, ID: q.ID, URL: q.OriginalURL, Mask: mask, Entry: v})
	case "save":
		if !e.owned {
			panic("quota save outside lock")
		}
		r.Err = e.save()
		if found := e.a.catalogQueryLocked(serviceCatalogQuery{Kind: 1, Index: e.catalogIndex}); found.Found {
			r.Entry = e.a.serviceStateLocked().catalogProjection(found.Entry, true)
		}

	default:
		panic("unknown quota effect: " + q.Kind)
	}
	return
}
func cloneQuotaEntries(src []subscriptionEntry) []subscriptionEntry {
	if src == nil {
		return nil
	}
	out := append([]subscriptionEntry{}, src...)
	for i := range out {
		if out[i].UserInfo != nil {
			v := *out[i].UserInfo
			out[i].UserInfo = &v
		}
	}
	return out
}
func (a *app) runQuotaService(q quotaServiceRequest) (out quotaServiceResult) {
	defer func() {
		if recover() != nil {
			out = quotaServiceResult{Status: 500, Message: "quota operation interrupted"}
		}
	}()
	if q.Op == "refresh" {
		q.ID = strings.TrimSpace(q.ID)
	}
	fx := newAppQuotaEffects(a)
	defer fx.Release()
	return runSelectedQuotaService(q, fx)
}
func writeQuotaServiceResult(w http.ResponseWriter, r quotaServiceResult) {
	if r.Status != 200 {
		http.Error(w, r.Message, r.Status)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "subscription": r.Entry})
}

var quotaServiceMessages = map[string]string{
	"missing-id": "missing subscription id",
	"not-found":  "没有这个订阅",
	"no-remote":  "此订阅没有可刷新的远程流量信息",
	"network":    "无法连接订阅服务，流量信息未刷新",
	"no-info":    "订阅服务未返回流量信息",
	"changed":    "订阅已变更，请重新刷新",
	"save-info":  "无法保存流量信息",
	"deleted":    "订阅已删除，请重新刷新",
	"save-error": "无法保存流量刷新状态",
}

func quotaFailure(code int, key string) quotaServiceResult {
	return quotaServiceResult{Status: code, Message: quotaServiceMessages[key]}
}
