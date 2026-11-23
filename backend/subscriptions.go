package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type subscriptionUserInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"`
}

// A saved subscription. The one in use is still settings.SubscriptionURL, so the
// rest of the pipeline — profile matching, the node cache, fetch and connect —
// is untouched; this list is only the register of what the user has saved.
type subscriptionEntry struct {
	ID             string                `json:"id"`
	URL            string                `json:"url"`
	Label          string                `json:"label"`
	Prefix         string                `json:"prefix,omitempty"`
	Enabled        bool                  `json:"enabled"`
	UpdatedAt      int64                 `json:"updatedAt,omitempty"`
	NodeCount      int                   `json:"nodeCount,omitempty"`
	LastError      string                `json:"lastError,omitempty"`
	UserInfo       *subscriptionUserInfo `json:"userInfo,omitempty"`
	QuotaUpdatedAt int64                 `json:"quotaUpdatedAt,omitempty"`
	QuotaError     string                `json:"quotaError,omitempty"`
}

const (
	subscriptionsKey      = "subscriptions"
	mergedSubscriptionURL = "__merged__"
)

// subscriptionLabel is what the list shows when the user has not renamed an
// entry: the host is enough to tell two providers apart.
func subscriptionLabel(rawURL string) string {
	remoteURLs, inlineLines := categorizeSubscriptionInput(rawURL)
	if len(remoteURLs)+len(inlineLines) > 1 {
		hosts := make([]string, 0, len(remoteURLs)+len(inlineLines))
		for _, u := range remoteURLs {
			parsed, err := url.Parse(u)
			if err == nil && parsed.Hostname() != "" {
				hosts = append(hosts, strings.TrimPrefix(parsed.Hostname(), "www."))
			}
		}
		for _, line := range inlineLines {
			if node, err := parseNodeURI(line); err == nil {
				hosts = append(hosts, node.Name)
			}
		}
		if len(hosts) > 0 {
			return strings.Join(hosts, " + ") + " (合并订阅)"
		}
		return fmt.Sprintf("合并订阅 (%d 个源)", len(remoteURLs)+len(inlineLines))
	}
	if len(inlineLines) == 1 && len(remoteURLs) == 0 {
		if node, err := parseNodeURI(inlineLines[0]); err == nil {
			return node.Name
		}
		return "直连节点"
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return rawURL
	}
	return strings.TrimPrefix(parsed.Hostname(), "www.")
}

func (a *app) loadSubscriptionsLocked() {
	a.runSubscriptionService(subscriptionServiceRequest{Op: "load"})
}

func (a *app) ensureSubscriptionLocked(rawURL string) {
	a.runSubscriptionService(subscriptionServiceRequest{Op: "ensure", URL: rawURL})
}

func (a *app) saveSubscriptionsLocked() error {
	if a.store == nil {
		return nil
	}
	return a.runSubscriptionService(subscriptionServiceRequest{Op: "save"}).Err
}

func (a *app) findSubscriptionLocked(id string) (subscriptionEntry, bool) {
	r := a.catalogQueryLocked(serviceCatalogQuery{Kind: 3, ID: id})
	return a.serviceStateLocked().unpackCatalog(r.Entry), r.Found
}
func (a *app) findSubscriptionByURL(rawURL string) (subscriptionEntry, bool) {
	url := rawURL
	r := a.catalogQueryLocked(serviceCatalogQuery{Kind: 4, URL: url, ID: subscriptionHash(url)})
	return a.serviceStateLocked().unpackCatalog(r.Entry), r.Found
}

func (a *app) profileCachePath(id string) string {
	return filepath.Join(a.home, "subscriptions", id+".yaml")
}

func (a *app) cacheProfileForID(id string, body []byte, format string) {
	a.runProfileService(profileServiceRequest{Op: "cache-id", ID: id, Body: body, Format: format})
}

func (a *app) cacheProfileLocked(body []byte, format string) {
	a.runProfileService(profileServiceRequest{Op: "cache-active", Body: body, Format: format})
}

// useCachedProfileLocked puts a subscription's cached profile in place and marks
// it as belonging to that subscription, which is what makes the rest of the
// pipeline accept it.
func (a *app) useCachedProfileLocked(id string) error {
	return a.runProfileService(profileServiceRequest{Op: "use-cache", ID: id}).Err
}

// clearNodeBoundStateLocked drops everything that described the nodes of the
// subscription we are leaving: node names, health, verified regions and the
// cached list. Keeping them would let an old, unreachable name look verified.
func (a *app) clearNodeBoundStateLocked() {
	a.patchControlLocked(controlSelectedNode, serviceControlState{SelectedNode: ""})
	a.patchControlLocked(controlSelectionMode, serviceControlState{SelectionMode: "auto"})
	a.patchControlLocked(controlLockedRegion, serviceControlState{LockedRegion: ""})
	a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: ""})
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: ""})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: ""})
	a.setHealthBlockedLocked(false)
	a.setHealthReasonLocked("")
	a.restoreHealthRecordsLocked(map[string]nodeHealth{})
	a.restoreRegionRecordsLocked(map[string]regionRecord{})
	a.clearCachedNodesLocked()
	if a.store != nil {
		if err := a.store.clearHealth(); err != nil {
			log.Printf("could not clear stored node health: %v", err)
		}
		if err := a.store.clearRegions(); err != nil {
			log.Printf("could not clear stored exit regions: %v", err)
		}
	}
	_ = a.saveSettings()
}

// ---------------------------------------------------------------------------
// HTTP

func (a *app) listSubscriptions(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(subscriptionServiceRequest{Op: "list"}))
}

func (a *app) startAutoMergeLoopLocked() {
	q := subscriptionHTTPServiceRequest{Op: "start"}
	e := newAppSubscriptionHTTPEffects(a, q)
	if r := runSubscriptionHTTPWithEffects(q, e); r.Err != nil {
		panic(r.Err)
	}
}

func (a *app) stopAutoMergeLoopLocked() {
	q := subscriptionHTTPServiceRequest{Op: "stop"}
	e := newAppSubscriptionHTTPEffects(a, q)
	if r := runSubscriptionHTTPWithEffects(q, e); r.Err != nil {
		panic(r.Err)
	}
}

func (a *app) autoMergeTickLocked() {
	a.runProfileService(profileServiceRequest{Op: "auto-tick"})
}

func (a *app) setAutoMergeHours(w http.ResponseWriter, r *http.Request) {

	var body struct {
		Hours int `json:"hours"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req := subscriptionServiceRequest{Op: "hours", Hours: body.Hours}
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(req))
}

func (a *app) activateSubscription(w http.ResponseWriter, r *http.Request) {

	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req := subscriptionServiceRequest{Op: "activate", ID: body.ID}
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(req))
}

func (a *app) recordSubscriptionUseLocked(id string, nodeCount int, failure string) {
	a.recordSubscriptionUseAndInfoLocked(id, nodeCount, failure, nil)
}

func (a *app) recordSubscriptionUseAndInfoLocked(id string, nodeCount int, failure string, info *subscriptionUserInfo) {
	a.runSubscriptionService(subscriptionServiceRequest{Op: "record-use", ID: id, NodeCount: nodeCount, Failure: failure, Info: info})
}

func (a *app) recordSubscriptionErrorLocked(id, failure string) {
	a.runSubscriptionService(subscriptionServiceRequest{Op: "record-error", ID: id, Failure: failure})
}

func (a *app) fetchSingleSubscriptionLocked(id string) (*subscriptionEntry, error) {
	r := a.runProfileService(profileServiceRequest{Op: "fetch-single", ID: id})
	return r.Entry, r.Err
}

func (a *app) mergeAllEnabledSubscriptionsLocked() ([]proxyNode, error) {
	r := a.runProfileService(profileServiceRequest{Op: "merge-all"})
	return r.Nodes, r.Err
}

// mergeEnabledSubscriptionCachesLocked reapplies the current source selection
// without downloading again. Every source included in a successful merge has
// its own last-good profile, which lets a single-source refresh update the live
// aggregate while offline sources keep their prior nodes.
func (a *app) mergeEnabledSubscriptionCachesLocked() ([]proxyNode, error) {
	r := a.runProfileService(profileServiceRequest{Op: "merge-caches"})
	return r.Nodes, r.Err
}

func (a *app) writeMergedSourcesLocked(sources []SubscriptionSource) ([]proxyNode, error) {
	r := a.runProfileService(profileServiceRequest{Op: "write-merged", Sources: sources})
	return r.Nodes, r.Err
}

func replaceSubscriptionProfile(path string, body []byte) error {
	s := profileServiceState{}
	return runSelectedProfileService(&s, profileServiceRequest{Op: "replace", Path: path, Body: body}, appProfileEffects{}).Err
}

func (a *app) addSubscription(w http.ResponseWriter, r *http.Request) {

	var body struct {
		URL     string `json:"url"`
		Label   string `json:"label"`
		Prefix  string `json:"prefix"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req := subscriptionServiceRequest{Op: "add", URL: body.URL, Label: body.Label, Prefix: body.Prefix, HasEnabled: body.Enabled != nil}
	if body.Enabled != nil {
		req.Enabled = *body.Enabled
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(req))
}

func (a *app) updateSubscription(w http.ResponseWriter, r *http.Request) {

	var body struct {
		ID      string  `json:"id"`
		URL     string  `json:"url,omitempty"`
		Label   string  `json:"label"`
		Prefix  *string `json:"prefix"`
		Enabled *bool   `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req := subscriptionServiceRequest{Op: "update", ID: body.ID, URL: body.URL, Label: body.Label, HasPrefix: body.Prefix != nil, HasEnabled: body.Enabled != nil}
	if body.Prefix != nil {
		req.Prefix = *body.Prefix
	}
	if body.Enabled != nil {
		req.Enabled = *body.Enabled
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(req))
}

func (a *app) mergeSubscriptions(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.runProfileService(profileServiceRequest{Op: "merge"})
	if out.Err != nil {
		http.Error(w, out.Err.Error(), out.Status)
		return
	}
	writeJSON(w, out.Data)
}

func (a *app) refreshSingleSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.runProfileService(profileServiceRequest{Op: "refresh-single", ID: body.ID})
	if out.Err != nil {
		http.Error(w, out.Err.Error(), out.Status)
		return
	}
	writeJSON(w, out.Data)
}

// refreshSubscriptionQuota reads only Subscription-Userinfo. It deliberately
// leaves profile caches and the running Mihomo configuration untouched.
func (a *app) refreshSubscriptionQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	writeQuotaServiceResult(w, a.runQuotaService(quotaServiceRequest{Op: "refresh", ID: body.ID}))
}

func (a *app) recordSubscriptionQuotaError(id, originalURL, message string, status int, w http.ResponseWriter) {
	writeQuotaServiceResult(w, a.runQuotaService(quotaServiceRequest{Op: "error", ID: id, OriginalURL: originalURL, Message: message, Status: status}))
}

func (a *app) renameSubscription(w http.ResponseWriter, r *http.Request) {

	var body struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req := subscriptionServiceRequest{Op: "rename", ID: body.ID, Label: body.Label}
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(req))
}

func (a *app) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeSubscriptionServiceResult(w, a.runSubscriptionService(subscriptionServiceRequest{Op: "delete", ID: r.URL.Query().Get("id")}))
}

func (a *app) cachedSources() []SubscriptionSource {
	return a.runProfileService(profileServiceRequest{Op: "cached-sources"}).Sources
}

func (a *app) labelSingleSource(nodes []proxyNode) {
	if a.controlMetaLocked().SubscriptionURL == "" || a.controlMetaLocked().SubscriptionURL == mergedSubscriptionURL {
		return
	}
	label := "当前订阅"
	for _, entry := range a.catalogEntriesLocked() {
		if entry.URL == a.controlMetaLocked().SubscriptionURL && entry.Label != "" {
			label = entry.Label
			break
		}
	}
	annotateProfileNodes(nodes, label, nil)
}

// Repair only missing metadata. Keep the active proxy list, order, user settings
// and database intact even if saved subscriptions have since been reordered.
func (a *app) restoreSourceLabels() { runAppLabelsService(a) }

func (a *app) autoMergeTickSafe() {
	q := subscriptionHTTPServiceRequest{Op: "tick"}
	e := newAppSubscriptionHTTPEffects(a, q)
	if r := runSubscriptionHTTPWithEffects(q, e); r.Err != nil {
		log.Print("automatic merge interrupted")
	}
}
