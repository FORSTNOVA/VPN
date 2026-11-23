package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real handler early rejections; no registered subscription means no HTTP port,
// profile/config IO, secret provider or registry persistence can be reached.
func TestMigrationQuotaHTTPRejectionsAndUnlock(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		message    string
	}{
		{"malformed", `{`, 400, "invalid request body\n"}, {"unknown-field", `{"id":"x","other":1}`, 400, "invalid request body\n"},
		{"wrong-type", `{"id":2}`, 400, "invalid request body\n"}, {"missing", `{}`, 400, "missing subscription id\n"}, {"null", `null`, 400, "missing subscription id\n"},
		{"empty", `{"id":" \n "}`, 400, "missing subscription id\n"}, {"not-found", `{"id":"synthetic-absent"}`, 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{}
			w := httptest.NewRecorder()
			a.refreshSubscriptionQuota(w, httptest.NewRequest("POST", "/api/subscriptions/quota", strings.NewReader(tc.body)))
			if w.Code != tc.status || (tc.message != "" && w.Body.String() != tc.message) {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			if !a.mu.TryLock() {
				t.Fatal("handler returned with application mutex locked")
			}
			a.mu.Unlock()
		})
	}
}

func TestMigrationQuotaHeaderLegacyContract(t *testing.T) {
	tests := []struct {
		header string
		want   *subscriptionUserInfo
	}{
		{"", nil}, {"garbage", nil}, {"upload=bad", nil}, {"unknown=9", &subscriptionUserInfo{}},
		{"unknown=bad", nil}, {"upload=-1; DOWNLOAD=2", &subscriptionUserInfo{Upload: -1, Download: 2}},
		{"total=9223372036854775807; expire=-9223372036854775808", &subscriptionUserInfo{Total: 9223372036854775807, Expire: -9223372036854775808}},
		{"total=9223372036854775808; upload=3", &subscriptionUserInfo{Upload: 3}},
		{"upload=1; upload=2; upload=invalid; ignored=6", &subscriptionUserInfo{Upload: 2}},
		{"download=9007199254740993", &subscriptionUserInfo{Download: 9007199254740993}},
	}
	for _, tc := range tests {
		if got := parseSubscriptionUserInfo(tc.header); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("header %q got=%+v want=%+v", tc.header, got, tc.want)
		}
	}
}

// The real HTTP adapter runs over net.Pipe, never an OS socket. Its Transport
// clone inherits only this DialContext. This verifies GET/header-only behavior,
// status handling and body closure without touching the network.
func TestMigrationQuotaHTTPAdapterMemoryPipe(t *testing.T) {
	for _, proxy := range []string{"direct", "http://synthetic-proxy.invalid:80", ""} {
		for _, tc := range []struct {
			name       string
			status     int
			header     string
			wantAbsent bool
		}{
			{"quota", 200, "upload=-3; total=9007199254740993", false},
			{"absent", 200, "", true}, {"non-success", 503, "upload=3", false},
		} {
			t.Run(proxy+"/"+tc.name, func(t *testing.T) {
				old := http.DefaultTransport
				var observed *http.Request
				observedCh := make(chan struct{})
				bodyWriteDone := make(chan struct{})
				var bodyWritten atomic.Int64
				transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {

					client, server := net.Pipe()
					go func() {
						defer server.Close()
						defer close(bodyWriteDone)
						request, err := http.ReadRequest(bufio.NewReader(server))
						if err == nil {
							observed = request
						}
						close(observedCh)
						if err != nil {
							return
						}
						response := fmt.Sprintf("HTTP/1.1 %d synthetic\r\nContent-Length: 1000000\r\nSubscription-Userinfo: %s\r\n\r\n", tc.status, tc.header)
						if _, err := io.WriteString(server, response); err != nil {
							return
						}
						n, _ := server.Write([]byte("must-not-read-body"))
						bodyWritten.Store(int64(n))
					}()
					return client, nil
				}}
				http.DefaultTransport = transport
				defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
				info, err := fetchSubscriptionUserInfo("http://synthetic-quota.invalid/sub", proxy)
				<-observedCh
				select {
				case <-bodyWriteDone:
				case <-time.After(time.Second):
					t.Fatal("response body was not closed")
				}
				if observed == nil || observed.Method != "GET" || observed.Header.Get("User-Agent") != subscriptionUserAgent {
					t.Fatalf("wrong request: %+v", observed)
				}

				if bodyWritten.Load() != 0 {
					t.Fatal("quota refresh read response body")
				}
				switch tc.name {
				case "quota":
					if err != nil || info == nil || info.Upload != -3 || info.Total != 9007199254740993 {
						t.Fatalf("quota=%+v err=%v", info, err)
					}
				case "absent":
					if !errors.Is(err, errNoSubscriptionUserInfo) {
						t.Fatalf("absent quota error=%v", err)
					}
				case "non-success":
					if err == nil || info != nil || errors.Is(err, errNoSubscriptionUserInfo) {
						t.Fatalf("non-success status contract info=%+v err=%v", info, err)
					}
				}
			})
		}
	}
}
