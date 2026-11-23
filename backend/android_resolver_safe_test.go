package main

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

type androidResolverTestConn struct{ closed int }

func (c *androidResolverTestConn) Read([]byte) (int, error)         { panic("unexpected read") }
func (c *androidResolverTestConn) Write([]byte) (int, error)        { panic("unexpected write") }
func (c *androidResolverTestConn) Close() error                     { c.closed++; return nil }
func (c *androidResolverTestConn) LocalAddr() net.Addr              { return nil }
func (c *androidResolverTestConn) RemoteAddr() net.Addr             { return nil }
func (c *androidResolverTestConn) SetDeadline(time.Time) error      { panic("unexpected deadline") }
func (c *androidResolverTestConn) SetReadDeadline(time.Time) error  { panic("unexpected deadline") }
func (c *androidResolverTestConn) SetWriteDeadline(time.Time) error { panic("unexpected deadline") }
func TestAndroidResolverServiceOriginalOrderAndIdentity(t *testing.T) {
	for _, success := range []int{-1, 0, 2, 4} {
		t.Run(string(rune('a'+success+1)), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			counter := uint32(0)
			servers := []string{"a", "b", "c", "d", "e"}
			var trace []string
			conn := &androidResolverTestConn{}
			errs := []error{errors.New("0"), errors.New("1"), errors.New("2"), errors.New("3"), errors.New("4")}
			got, err := dialAndroidResolver(ctx, "tcp", servers, &counter, func(c context.Context, n, s string, timeout time.Duration) (net.Conn, error) {
				if c != ctx || n != "tcp" || timeout != 2500*time.Millisecond {
					t.Fatal("dial arguments changed")
				}
				i := len(trace)
				trace = append(trace, s)
				if i == success {
					return conn, nil
				}
				return conn, errs[i]
			})
			count := 5
			if success >= 0 {
				count = success + 1
				if got != conn || err != nil {
					t.Fatal("success identity lost")
				}
			} else if got != nil || err != errs[4] {
				t.Fatal("last error identity lost")
			}
			if !reflect.DeepEqual(trace, []string{"b", "c", "d", "e", "a"}[:count]) || counter != 1 || conn.closed != 0 {
				t.Fatal("rotation/resource behavior changed")
			}
		})
	}
}
func TestAndroidResolverServiceWrapAndNilConnection(t *testing.T) {
	counter := ^uint32(0)
	calls := 0
	got, err := dialAndroidResolver(context.Background(), "udp", []string{"a", "b", "c", "d", "e"}, &counter, func(_ context.Context, _ string, s string, _ time.Duration) (net.Conn, error) {
		calls++
		if s != "a" {
			t.Fatal("wrap started wrong")
		}
		return nil, nil
	})
	if got != nil || err != nil || counter != 0 || calls != 1 {
		t.Fatal("nil-connection success or wrap changed")
	}
}
