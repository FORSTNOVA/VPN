//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

type kernelAdmissionWindowsEffects struct {
	e                 *appKernelAdmissionEffects
	k                 *execKernel
	runner            *execKernel
	cmd               *exec.Cmd
	file              *os.File
	log               io.WriteCloser
	logOwned, started bool
	start, wait, kill func() error
	spawn             func(func())
}

func newWindowsKernelAdmission(a *app, k *execKernel, q kernelAdmissionRequest) (*appKernelAdmissionEffects, *kernelAdmissionWindowsEffects) {
	e := newKernelAdmissionEffects(a, q)
	w := &kernelAdmissionWindowsEffects{e: e, k: k, spawn: func(f func()) { go f() }}
	e.platform = w.Call
	e.cleanup = w.Release
	return e, w
}
func (w *kernelAdmissionWindowsEffects) closeLog() {
	if w.logOwned {
		w.logOwned = false
		if w.log != nil {
			_ = w.log.Close()
		}
	}
}
func (w *kernelAdmissionWindowsEffects) Release() {
	defer w.closeLog()
	// Only a successfully acknowledged Start on this invocation is ours.
	// Never infer ownership from k.cmd, which can be a prior/published runner.
	if w.started {
		w.started = false
		if w.kill != nil && w.kill() == nil && w.wait != nil {
			_ = w.wait()
		}
	}
}
func (w *kernelAdmissionWindowsEffects) Call(q kernelAdmissionEffect) (r kernelAdmissionReply) {
	e := w.e
	k := w.k
	switch q.Kind {
	case "runner-new":
		w.runner = &execKernel{app: e.a, clashRS: clashRSFilename(q.Text), path: q.Text, config: e.a.configPath()}
	case "running":
		r.Bool = k.cmd != nil && k.cmd.Process != nil
	case "command-new":
		w.cmd = exec.Command(k.path, kernelArgs(k.clashRS, "-d", k.app.home, "-f", k.config)...)
		w.cmd.Dir = k.app.home
		w.start = w.cmd.Start
		w.wait = w.cmd.Wait
		w.kill = func() error { return w.cmd.Process.Kill() }
	case "log-open":
		w.file, r.Err = os.OpenFile(filepath.Join(k.app.home, "mihomo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if r.Err == nil {
			w.log = w.file
			w.logOwned = true
		}
	case "command-log":
		w.cmd.Stdout, w.cmd.Stderr = w.log, w.log
	case "process-start":
		r.Err = w.start()
		if r.Err == nil {
			w.started = true
		}
	case "publish":
		k.cmd, k.log = w.cmd, w.file
	case "exited-new":
		k.exited = make(chan struct{})
	case "watch-spawn":
		child, cw := newWindowsKernelAdmission(k.app, k, kernelAdmissionRequest{Op: "watch"})
		child.hooks = e.hooks
		cw.cmd = w.cmd
		cw.file = w.file
		cw.log = w.log
		cw.logOwned = w.logOwned
		cw.wait = w.wait
		// Consume ownership before scheduling: an unknown spawn acknowledgement
		// must not cause a second Wait/Kill/Close alongside an existing watcher.
		w.started = false
		w.logOwned = false
		w.spawn(func() {
			if x := runKernelAdmissionWithEffects(child.q, child); x.Err != nil {
				log.Printf("kernel watcher incomplete: %v", x.Err)
			}
		})
	case "process-wait":
		r.Err = w.wait()
	case "log-close":
		w.closeLog()
	case "exited-close":
		close(k.exited)
	case "exit-callback":
		k.app.kernelExited(k)
	case "process-kill":
		r.Err = k.cmd.Process.Kill()
	case "exited-join":
		<-k.exited
	case "netstat":
		cmd := exec.CommandContext(e.ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "netstat.exe"), "-ano", "-p", "tcp")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		e.output, r.Err = cmd.Output()
	case "pid":
		if k.managed != nil {
			r.Text = strconv.Itoa(k.managed.pid)
		} else {
			r.Text = strconv.Itoa(k.cmd.Process.Pid)
		}
	case "interface":
		a, err := net.InterfaceByName(tunInterfaceName)
		r.Err = err
		if err == nil {
			r.Bool = a.Flags&net.FlagUp != 0
			r.Count = int(uint32(a.Index))
		}
	case "route4":
		var i uint32
		r.Err = windows.GetBestInterfaceEx(&windows.SockaddrInet4{Addr: [4]byte{1, 1, 1, 1}}, &i)
		r.Count = int(i)
	case "route6":
		a := &windows.SockaddrInet6{}
		copy(a.Addr[:], net.ParseIP("2606:4700:4700::1111").To16())
		var i uint32
		r.Err = windows.GetBestInterfaceEx(a, &i)
		r.Count = int(i)
		r.Bool = kernelAdmissionIPv6Unavailable(r.Err)
	case "config-command":
		cmd := exec.CommandContext(e.ctx, q.Text, kernelArgs(clashRSFilename(q.Text), "-t", "-d", e.a.home, "-f", e.q.Name)...)
		cmd.Dir = e.a.home
		e.output, r.Err = cmd.CombinedOutput()
	default:
		panic("unknown Windows admission effect")
	}
	return
}
func (a *app) runWindowsKernelAdmission(k *execKernel, q kernelAdmissionRequest) kernelAdmissionResult {
	e, _ := newWindowsKernelAdmission(a, k, q)
	return runKernelAdmissionWithEffects(q, e)
}

// Preserve errors.Is semantics and the original four-class evaluation order.
func kernelAdmissionIPv6Unavailable(err error) bool {
	return errors.Is(err, windows.ERROR_NETWORK_UNREACHABLE) || errors.Is(err, windows.ERROR_HOST_UNREACHABLE) || errors.Is(err, windows.ERROR_NOT_FOUND) || errors.Is(err, windows.ERROR_NOT_SUPPORTED)
}
