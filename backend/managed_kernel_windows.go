package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type managedKernelProcess struct {
	pid       int
	control   io.WriteCloser
	reloadKey string
	target    monitorTarget
	cancel    context.CancelFunc
}

func managedHostPath() (string, error) {
	path := os.Getenv("SMARTVPN_MANAGED_HOST")
	if path == "off" {
		return "", nil
	}
	if path != "" {
		if !filepath.IsAbs(path) {
			return "", errors.New("SMARTVPN_MANAGED_HOST requires an absolute path")
		}
		return path, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path = filepath.Join(filepath.Dir(exe), "smartvpn-host.exe")
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return path, err
}

func (k *execKernel) startManaged(host string) error {
	if k.managed != nil || k.Running() {
		return errors.New("kernel runner already used")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(key[:])
	kernel, err := filepath.Abs(k.path)
	if err != nil {
		return err
	}
	home, err := filepath.Abs(k.app.home)
	if err != nil {
		return err
	}
	config, err := filepath.Abs(k.config)
	if err != nil {
		return err
	}
	timeout := k.app.healthMetaLocked().Params.ConnectTimeoutMs
	if timeout < 1000 || timeout > 15000 {
		timeout = 3000
	}
	reloadKey := ""
	if windowsKernelHostEnabled() {
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		reloadKey = hex.EncodeToString(key[:])
	}
	cmd := exec.Command(host, "--managed", kernel, home, config, strconv.Itoa(k.app.ctrlPort))
	cmd.Dir = home
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = append(os.Environ(), "SMARTVPN_MONITOR_TOKEN="+token, "SMARTVPN_MONITOR_CONTROLLER_SECRET="+k.app.ctrlSecret, "SMARTVPN_MONITOR_NODE_TIMEOUT_MS="+strconv.Itoa(timeout))
	cmd.Env = append(cmd.Env, "SMARTVPN_MANAGED_RELOAD_TOKEN="+reloadKey)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return err
	}
	type ready struct {
		reload bool
		pid    int
		addr   string
	}
	ack := make(chan ready, 1)
	go func() {
		reader := bufio.NewReaderSize(out, 256)
		pidLine, e1 := reader.ReadSlice('\n')
		pid, e2 := strconv.ParseUint(strings.TrimSpace(string(pidLine)), 10, 32)
		addrLine, e3 := reader.ReadSlice('\n')
		addr := strings.TrimSpace(string(addrLine))
		addr, v2 := strings.CutPrefix(addr, "v2 ")
		if v2 && reloadKey == "" {
			ack <- ready{}
			return
		}
		ip, port, e4 := net.SplitHostPort(addr)
		n, e5 := strconv.ParseUint(port, 10, 16)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || pid == 0 || n == 0 || ip != "127.0.0.1" {
			ack <- ready{}
			return
		}
		ack <- ready{v2, int(pid), addr}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var started ready
	select {
	case started = <-ack:
	case <-timer.C:
	}
	if started.pid == 0 {
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("Rust supervisor did not publish a kernel PID and monitor address")
	}
	ctx, cancel := context.WithCancel(context.Background())
	k.cmd = cmd
	k.exited = make(chan struct{})
	if !started.reload {
		reloadKey = ""
	}
	k.managed = &managedKernelProcess{reloadKey: reloadKey, pid: started.pid, control: in, target: monitorTarget{addr: started.addr, token: token, lifetime: ctx}, cancel: cancel}
	go func() {
		_ = cmd.Wait()
		cancel()
		close(k.exited)
		k.app.kernelExited(k)
	}()
	return nil
}

func (k *execKernel) stopManaged() error {
	k.managed.cancel()
	_ = k.managed.control.Close()
	select {
	case <-k.exited:
		return nil
	case <-time.After(3 * time.Second):
		if err := k.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop Rust supervisor: %w", err)
		}
		<-k.exited
		return nil
	}
}
func (k *execKernel) monitorEndpoint() (monitorTarget, bool) {
	if k.managed == nil || !k.Running() {
		return monitorTarget{}, false
	}
	return k.managed.target, true
}
