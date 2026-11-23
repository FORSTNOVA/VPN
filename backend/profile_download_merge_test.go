package main

import (
	"gopkg.in/yaml.v3"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSubscriptionDownloadAndMergeRules(t *testing.T) {
	const body = "proxies:\n  - {name: n, type: http, server: 127.0.0.1, port: 8080}\nrules:\n  - DOMAIN,example.test, n ,no-resolve\n  - MATCH,DIRECT\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "UPLOAD=1; upload=2; download=3; broken=oops")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	downloaded, nodes, info, err := downloadProfileWithInfo(server.URL, "direct")
	if err != nil || len(nodes) != 1 || info == nil || *info != (subscriptionUserInfo{Upload: 2, Download: 3}) {
		t.Fatalf("download nodes=%d info=%+v err=%v", len(nodes), info, err)
	}
	merged, nodes, err := mergeSubscriptionSources([]SubscriptionSource{{Label: "one", Prefix: "A", Body: downloaded}, {Label: "two", Prefix: "B", Body: downloaded}})
	var out struct {
		Rules []string `yaml:"smartvpn-source-rules"`
	}
	if err != nil || len(nodes) != 2 || yaml.Unmarshal(merged, &out) != nil {
		t.Fatalf("merge nodes=%d err=%v", len(nodes), err)
	}
	want := []string{"DOMAIN,example.test,A n,no-resolve", "MATCH,DIRECT", "DOMAIN,example.test,B n,no-resolve", "MATCH,DIRECT"}
	if !reflect.DeepEqual(out.Rules, want) {
		t.Fatalf("rules %q want %q", out.Rules, want)
	}
}
