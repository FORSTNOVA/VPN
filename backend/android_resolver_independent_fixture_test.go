package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"
)

type resolverIndependentFX struct {
	Count, Advance int64
	Success        int
	Calls          int
	Trace          []androidResolverEffect
}

func (f *resolverIndependentFX) Release() {}
func (f *resolverIndependentFX) Call(e androidResolverEffect) androidResolverReply {
	f.Trace = append(f.Trace, e)
	switch e.Kind {
	case "count":
		return androidResolverReply{Number: f.Count}
	case "advance":
		return androidResolverReply{Number: f.Advance}
	case "dial":
		f.Calls++
		if f.Calls == f.Success {
			return androidResolverReply{Flag: true}
		}
		return androidResolverReply{Err: adaptiveIndependentErr}
	default:
		panic("unsafe resolver port")
	}
}

type resolverIndependentRunner func(androidResolverRequest, androidResolverEffects) androidResolverResult

func resolverIndependentMatrix(t *testing.T, run resolverIndependentRunner) {
	for i := 0; i < 600; i++ {
		a := &resolverIndependentFX{Count: int64(i%7 + 1), Advance: int64(uint32(i * 43217)), Success: i % 9}
		b := *a
		ga := runGoAndroidResolver(androidResolverRequest{Op: "dial"}, a)
		gb := run(androidResolverRequest{Op: "dial"}, &b)
		if !reflect.DeepEqual(ga, gb) || !reflect.DeepEqual(a, &b) {
			t.Fatalf("resolver%d %+v %+v", i, a, b)
		}
	}
}
func resolverIndependentResources(t *testing.T, run resolverIndependentRunner) {
	for _, start := range []uint32{0, 4, ^uint32(0)} {
		for _, success := range []int{0, 1, 3} {
			t.Run(fmt.Sprintf("%d-%d", start, success), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				counter := start
				calls := []string{}
				errs := []error{errors.New("first"), errors.New("second"), errors.New("last")}
				servers := []string{"one.invalid:53", "two.invalid:53", "three.invalid:53"}
				e := &appAndroidResolverEffects{ctx: ctx, network: "udp6", servers: servers, counter: &counter, dial: func(got context.Context, n, s string, d time.Duration) (net.Conn, error) {
					if got != ctx || n != "udp6" || d != 2500*time.Millisecond {
						t.Fatal("context/network/deadline")
					}
					calls = append(calls, s)
					if len(calls) == success {
						return nil, nil
					}
					return nil, errs[len(calls)-1]
				}}
				r := run(androidResolverRequest{Op: "dial"}, e)
				wantCount := success
				if success == 0 {
					wantCount = 3
					if r.Err != errs[2] {
						t.Fatal("last error identity")
					}
				} else if r.Err != nil || r.Status <= 0 || e.conn != nil {
					t.Fatal("nil connection successful pair")
				}
				if len(calls) != wantCount || counter != start+1 {
					t.Fatal("advance once")
				}
				for i, s := range calls {
					if s != servers[(int(counter)%3+i)%3] {
						t.Fatal("roundrobin order", calls)
					}
				}
			})
		}
	}
}
func TestAndroidResolverServiceIndependentMatrix(t *testing.T) {
	resolverIndependentMatrix(t, runSelectedAndroidResolver)
}
func TestAndroidResolverServiceIndependentResources(t *testing.T) {
	resolverIndependentResources(t, runSelectedAndroidResolver)
}
