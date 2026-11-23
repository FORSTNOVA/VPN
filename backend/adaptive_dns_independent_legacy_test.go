package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/miekg/dns"
	"io"
	"net/http"
	"testing"
)

type dnsIndependentRT func(*http.Request) (*http.Response, error)

func (f dnsIndependentRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type dnsIndependentBody struct {
	io.Reader
	reads, closed int
	err           error
}

func (b *dnsIndependentBody) Read(p []byte) (int, error) {
	b.reads++
	n, e := b.Reader.Read(p)
	if b.err != nil {
		return n, b.err
	}
	return n, e
}
func (b *dnsIndependentBody) Close() error { b.closed++; return nil }
func adaptiveIndependentExchangeGoldens(t *testing.T, run adaptiveIndependentRunner) {
	for _, kind := range []string{"valid", "nxdomain", "not-response", "id", "question", "two-questions", "servfail", "refused", "http", "unpack", "read", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			q := new(dns.Msg).SetQuestion("Example.test.", dns.TypeTXT)
			q.Id = 37
			a := new(dns.Msg).SetReply(q)
			a.Question = append([]dns.Question(nil), q.Question...)
			switch kind {
			case "nxdomain":
				a.Rcode = dns.RcodeNameError
			case "not-response":
				a.Response = false
			case "id":
				a.Id++
			case "question":
				a.Question[0].Name = "example.test."
			case "two-questions":
				a.Question = append(a.Question, a.Question[0])
			case "servfail":
				a.Rcode = dns.RcodeServerFailure
			case "refused":
				a.Rcode = dns.RcodeRefused
			}
			raw, err := a.Pack()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unpack" {
				raw = []byte{1}
			}
			if kind == "oversize" {
				raw = append(raw, bytes.Repeat([]byte{0}, 65536)...)
			}
			body := &dnsIndependentBody{Reader: bytes.NewReader(raw)}
			sentinel := errors.New("synthetic body read")
			if kind == "read" {
				body.err = sentinel
			}
			calls := 0
			r := &adaptiveDNS{client: &http.Client{Transport: dnsIndependentRT(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.String() != "https://1.1.1.1/dns-query" || req.Header.Get("Content-Type") != "application/dns-message" || req.Header.Get("Accept") != "application/dns-message" {
					t.Fatal("request contract")
				}
				packet, e := io.ReadAll(req.Body)
				if e != nil {
					t.Fatal(e)
				}
				var sent dns.Msg
				if sent.Unpack(packet) != nil || sent.Id != q.Id {
					t.Fatal("DNS packet")
				}
				code := 200
				if kind == "http" {
					code = 503
				}
				return &http.Response{StatusCode: code, Body: body}, nil
			})}}
			e := newAdaptiveEffects(r, nil, adaptiveServiceRequest{Op: "exchange", Index: 0})
			e.query = q
			e.ctx = context.Background()
			result := run(e.q, e)
			out, err := e.answer, result.Err
			success := kind == "valid" || kind == "nxdomain"
			if (err == nil) != success || (out != nil) != success || calls != 1 || body.closed != 1 {
				t.Fatalf("case%s answer%v err%v calls%d close%d", kind, out, err, calls, body.closed)
			}
			if kind == "http" && (body.reads != 0 || err.Error() != "DoH HTTP 503") {
				t.Fatal("status before read")
			}
			if kind == "read" && !errors.Is(err, sentinel) {
				t.Fatal("read error priority", err)
			}
		})
	}
}

func TestAdaptiveDNSIndependentLegacyExchange(t *testing.T) {
	adaptiveIndependentExchangeGoldens(t, runSelectedAdaptiveService)
}
