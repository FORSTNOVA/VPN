//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// execKernel is the kernel as a separate process, which is how Windows runs it.
// It is a process rather than a library because the kernel needs an
// administrator token to create its TUN adapter, and the window must not have
// to hold one; the child inherits only what it needs.
type execKernel struct {
	app    *app
	path   string
	cmd    *exec.Cmd
	exited chan struct{}
	log    *os.File
	config string
}

// newKernel finds the kernel this copy will run: what the user chose, otherwise
// the copy that came with the package, otherwise the profile.
func (a *app) newKernel() (kernelRunner, error) {
	kernel := a.mihomoPath()
	if _, err := os.Stat(kernel); err != nil {
		return nil, fmt.Errorf("没找到 Mihomo 内核：%s（把它放在这个位置，或在「订阅与内核」里填写路径）", kernel)
	}
	return &execKernel{app: a, path: kernel, config: a.configPath()}, nil
}

func (k *execKernel) Running() bool {
	return k.cmd != nil && k.cmd.Process != nil
}

func (k *execKernel) Start() error {
	cmd := exec.Command(k.path, "-d", k.app.home, "-f", k.config)
	cmd.Dir = k.app.home
	logFile, err := os.OpenFile(filepath.Join(k.app.home, "mihomo.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("could not open Mihomo log")
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return errors.New("could not start Mihomo")
	}
	k.cmd, k.log = cmd, logFile
	k.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(k.exited)
		k.app.kernelExited(k)
	}()
	return nil
}

func (k *execKernel) Stop() error {
	if !k.Running() {
		return nil
	}
	err := k.cmd.Process.Kill()
	if err == nil {
		// Kill on Windows returns before the process releases its listener.
		// Reconnect must be able to bind that same port immediately.
		<-k.exited
	}
	return err
}

// A successful dial can belong to another program that bound the fixed port
// after our availability check. Only publish the proxy once its PID matches.
func (k *execKernel) verifyPortOwner(port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "netstat.exe"), "-ano", "-p", "tcp")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("无法核对 SOCKS5 端口 %d 的监听进程：%w", port, err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 5 && f[1] == addr && f[2] == "0.0.0.0:0" && f[4] == strconv.Itoa(k.cmd.Process.Pid) {
			return nil
		}
	}
	return fmt.Errorf("内核未能监听 SOCKS5 端口 %s，请检查端口占用后重连", addr)
}

// checkKernelConfig hands a configuration to the kernel and reports whether it
// accepts it, without connecting. The kernel has a mode for exactly this: it
// parses everything, resolves the providers and exits.
//
// This exists because a kernel that refuses a configuration starts and dies,
// and the alternative to asking first is a connect that fails with nothing to
// go on. An extra process is a real cost on Windows, so it is spent only where
// the answer can change what is generated — the domestic address rules.
func (a *app) checkKernelConfig(configPath string) error {
	kernel := a.mihomoPath()
	if kernel == "" {
		return errors.New("no kernel to check the configuration with")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, kernel, "-t", "-d", a.home, "-f", configPath)
	cmd.Dir = a.home
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, lastLine(string(output)))
	}
	return nil
}
