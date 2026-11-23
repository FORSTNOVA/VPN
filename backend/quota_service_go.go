package main

import (
	"errors"
)

func runGoQuotaService(q quotaServiceRequest, fx quotaServiceEffects) quotaServiceResult {
	defer fx.Release()
	original := q.OriginalURL
	message := q.Message
	status := q.Status
	var info *subscriptionUserInfo
	if q.Op == "refresh" {
		if q.ID == "" {
			return quotaFailure(400, "missing-id")
		}
		entries := fx.Call(quotaEffect{Kind: "lock", ID: q.ID}).Entries
		var entry subscriptionEntry
		found := false
		for _, v := range entries {
			if v.ID == q.ID {
				entry = v
				found = true
				break
			}
		}
		if !found {
			fx.Call(quotaEffect{Kind: "unlock"})
			return quotaFailure(404, "not-found")
		}
		original = entry.URL
		urls := fx.Call(quotaEffect{Kind: "categorize", URL: entry.URL}).Strings
		proxies := fx.Call(quotaEffect{Kind: "proxies"}).Strings
		fx.Call(quotaEffect{Kind: "unlock"})
		if len(urls) == 0 {
			message = quotaServiceMessages["no-remote"]
			status = 400
		} else {
			noInfo := false
			for _, remote := range urls {
				candidates := fx.Call(quotaEffect{Kind: "candidates", URL: remote}).Strings
				for _, proxy := range proxies {
					for _, candidate := range candidates {
						out := fx.Call(quotaEffect{Kind: "fetch", URL: candidate, Proxy: proxy})
						if out.Err == nil {
							info = out.Info
							break
						}
						if errors.Is(out.Err, errNoSubscriptionUserInfo) {
							noInfo = true
						}
					}
					if info != nil {
						break
					}
				}
				if info != nil {
					break
				}
			}
			if info == nil {
				status = 502
				message = quotaServiceMessages["network"]
				if noInfo {
					message = quotaServiceMessages["no-info"]
				}
			}
		}
	} else if q.Op != "error" {
		panic("unknown quota operation: " + q.Op)
	}
	current := fx.Call(quotaEffect{Kind: "lock", ID: q.ID}).Entries
	for i, v := range current {
		if v.ID != q.ID {
			continue
		}
		if v.URL != original {
			return quotaFailure(409, "changed")
		}
		effect := quotaEffect{Kind: "commit", Index: i, ID: q.ID, OriginalURL: original, Message: message, Info: info, Success: info != nil}
		if info != nil {
			effect.Timestamp = fx.Call(quotaEffect{Kind: "now"}).Timestamp
		}
		fx.Call(effect)
		saved := fx.Call(quotaEffect{Kind: "save", Index: i})
		if saved.Err != nil {
			if info != nil {
				return quotaFailure(500, "save-info")
			}
			return quotaFailure(500, "save-error")
		}
		if info != nil {
			return quotaServiceResult{Status: 200, Entry: saved.Entry, HasEntry: true}
		}
		return quotaServiceResult{Status: status, Message: message}
	}
	return quotaFailure(409, "deleted")
}
