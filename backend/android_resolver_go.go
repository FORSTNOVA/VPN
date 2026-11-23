package main

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

func runGoAndroidResolver(q androidResolverRequest, fx androidResolverEffects) androidResolverResult {
	a := androidResolverOracle{fx}
	count := int(a.call("count", "", "", 0).Number)
	start := int(a.call("advance", "", "", 0).Number) % count
	var lastErr error
	for i := 0; i < count; i++ {
		index := (start + i) % count
		if index < 0 {
			panic("negative resolver index")
		}
		r := a.call("dial", "2500", "", int64(index))
		if r.Err == nil {
			return androidResolverResult{Status: index + 1}
		}
		lastErr = r.Err
	}
	return androidResolverResult{Err: lastErr}
}

type appAndroidResolverEffects struct {
	ctx     context.Context
	network string
	servers []string
	counter *uint32
	dial    func(context.Context, string, string, time.Duration) (net.Conn, error)
	conn    net.Conn
}

func (f *appAndroidResolverEffects) Release() {}
func (f *appAndroidResolverEffects) Call(e androidResolverEffect) androidResolverReply {
	switch e.Kind {
	case "count":
		return androidResolverReply{Number: int64(len(f.servers))}
	case "advance":
		return androidResolverReply{Number: int64(int(atomic.AddUint32(f.counter, 1)))}
	case "dial":
		ms, err := strconv.ParseInt(e.Text, 10, 64)
		if err != nil {
			panic(err)
		}
		conn, err := f.dial(f.ctx, f.network, f.servers[int(e.Number)], time.Duration(ms)*time.Millisecond)
		if err == nil {
			f.conn = conn
		}
		return androidResolverReply{Flag: err == nil, Err: err}
	}
	panic("unexpected resolver effect")
}
func dialAndroidResolver(ctx context.Context, network string, servers []string, counter *uint32, dial func(context.Context, string, string, time.Duration) (net.Conn, error)) (net.Conn, error) {
	fx := &appAndroidResolverEffects{ctx: ctx, network: network, servers: servers, counter: counter, dial: dial}
	r := runSelectedAndroidResolver(androidResolverRequest{Op: "dial"}, fx)
	if r.Status > 0 {
		return fx.conn, nil
	}
	return nil, r.Err
}
