//go:build android

package main

import (
	"fmt"
	"github.com/metacubex/mihomo/common/observable"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"
	listenerconfig "github.com/metacubex/mihomo/listener/config"
	mihomolog "github.com/metacubex/mihomo/log"
	mihomotunnel "github.com/metacubex/mihomo/tunnel"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type appEmbeddedKernelEffects struct {
	k                   *embeddedKernel
	a                   *app
	q                   embeddedKernelRequest
	home, path, payload string
	body                []byte
	file                *os.File
	sub                 observable.Subscription[mihomolog.Event]
	redactor            *logRedactor
	event               mihomolog.Event
	apply               func(bool)
	owned               bool
}

func (f *appEmbeddedKernelEffects) Release() {
	if f.q.Op == "running" && f.owned {
		f.owned = false
		f.k.mu.Unlock()
	}
}
func (f *appEmbeddedKernelEffects) Call(e embeddedKernelEffect) embeddedKernelReply {
	switch e.Kind {
	case "chdir":
		return embeddedKernelReply{Err: os.Chdir(f.home)}
	case "home":
		constant.SetHomeDir(f.home)
	case "mkdir":
		return embeddedKernelReply{Err: os.MkdirAll(f.home, os.FileMode(e.Number))}
	case "read-config":
		path := f.path
		if f.q.Op == "start" || f.q.Op == "session-start" {
			path = f.k.app.configPath()
		}
		b, err := os.ReadFile(path)
		f.body = b
		return embeddedKernelReply{Err: err}
	case "start-logging":
		f.k.startLogging()
	case "stop-logging":
		f.k.stopLogging()
	case "hub-parse":
		return embeddedKernelReply{Err: hub.Parse(f.body)}
	case "refusal":
		return embeddedKernelReply{Err: f.k.tunnelRefused()}
	case "shutdown":
		// Cleanup closes the device but retains LastTunConf. Android can reuse
		// the same descriptor on restart, so apply a disabled configuration
		// before shutdown or ReCreateTun can skip opening the new device.
		listener.ReCreateTun(listenerconfig.Tun{}, mihomotunnel.Tunnel)
		executor.Shutdown()
	case "handover":
		f.k.generation = tunnel.handOver()
	case "start-monitor":
		f.k.monitor = f.k.startMonitor()
	case "stop-monitor":
		f.k.stopMonitor()
	case "check-generation":
		if f.k == nil || !tunnel.ownedBy(f.k.generation) {
			return embeddedKernelReply{Err: fmt.Errorf("Android tunnel generation is no longer active")}
		}
	case "lock":
		f.k.mu.Lock()
		f.owned = true
	case "unlock":
		f.owned = false
		f.k.mu.Unlock()
	case "running":
		return embeddedKernelReply{Flag: f.k.running}
	case "set-running":
		f.k.running = e.Number != 0
	case "sleep":
		time.Sleep(time.Duration(e.Number) * time.Millisecond)
	case "tun-error":
		return embeddedKernelReply{Text: f.k.tunError}
	case "open-log":
		file, err := os.OpenFile(filepath.Join(f.k.app.home, "mihomo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, os.FileMode(e.Number))
		f.file = file
		return embeddedKernelReply{Err: err}
	case "redactor":
		f.redactor = newLogRedactor(f.file)
		f.redactor.Set(f.k.app.subscriptionSecrets())
	case "subscribe":
		f.sub = mihomolog.Subscribe()
	case "publish-log":
		f.k.logFile, f.k.logSub = f.file, f.sub
	case "clear-tun-error":
		f.k.tunError = ""
	case "spawn-logs":
		k, sub, redactor := f.k, f.sub, f.redactor
		go func() {
			q := embeddedKernelRequest{Op: "logs"}
			runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, q: q, sub: sub, redactor: redactor})
		}()
	case "next-event":
		event, ok := <-f.sub
		f.event = event
		return embeddedKernelReply{Flag: ok}
	case "handle-event":
		q := embeddedKernelRequest{Op: "event"}
		runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: f.k, q: q, event: f.event, redactor: f.redactor})
	case "event-facts":
		return embeddedKernelReply{Number: int64(strings.Index(f.event.Payload, "TUN listening error"))}
	case "tun-empty":
		return embeddedKernelReply{Flag: f.k.tunError == ""}
	case "capture-tun":
		f.k.tunError = f.event.Payload
	case "write-log":
		_, _ = f.redactor.Write([]byte(fmt.Sprintf("time=\"%s\" level=%s msg=%q\n", time.Now().Format(time.RFC3339), f.event.LogLevel.String(), f.event.Payload)))
	case "snapshot-log":
		f.file, f.sub = f.k.logFile, f.k.logSub
		mask := int64(0)
		if f.sub != nil {
			mask |= 1
		}
		if f.file != nil {
			mask |= 2
		}
		return embeddedKernelReply{Number: mask}
	case "clear-log":
		f.k.logFile, f.k.logSub = nil, nil
	case "unsubscribe":
		mihomolog.UnSubscribe(f.sub)
	case "close-log":
		_ = f.file.Close()
	case "parse-config":
		_, err := executor.ParseWithBytes(f.body)
		return embeddedKernelReply{Err: err}
	case "parse-live":
		cfg, err := executor.ParseWithBytes([]byte(f.payload))
		if err == nil && f.q.Op == "session-apply" && (!cfg.General.Tun.Enable || cfg.General.Tun.FileDescriptor != tunnel.descriptor()) {
			return embeddedKernelReply{Err: fmt.Errorf("live config must retain the active Android tunnel descriptor")}
		}
		if err == nil {
			f.apply = func(force bool) { executor.ApplyConfig(cfg, force) }
		}
		return embeddedKernelReply{Err: err}
	case "apply-config":
		f.apply(e.Number != 0)
	default:
		panic("unexpected embedded kernel resource")
	}
	return embeddedKernelReply{}
}
