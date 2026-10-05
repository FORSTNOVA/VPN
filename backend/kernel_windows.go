//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		k.app.kernelExited(k)
	}()
	return nil
}

func (k *execKernel) Stop() error {
	if !k.Running() {
		return nil
	}
	return k.cmd.Process.Kill()
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
