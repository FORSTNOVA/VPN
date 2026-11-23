package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func startAPIHost(port int, key, token string, writes bool) (string, func(), error) {
	path, err := managedHostPath()
	if err != nil {
		return "", nil, err
	}
	if path == "" {
		return "", nil, errors.New("no read host installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	mode := "--reads"
	if writes {
		mode = "--api"
	}
	cmd := exec.CommandContext(ctx, path, mode, strconv.Itoa(port))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = append(os.Environ(), "SMARTVPN_RESOURCE_TOKEN="+key, "SMARTVPN_MONITOR_TOKEN="+token)
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return "", nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		cancel()
		return "", nil, err
	}
	if err = cmd.Start(); err != nil {
		out.Close()
		in.Close()
		cancel()
		return "", nil, err
	}
	ready := make(chan string, 1)
	go func() {
		line, err := bufio.NewReaderSize(out, 256).ReadSlice('\n')
		if err != nil {
			ready <- ""
			return
		}
		ready <- strings.TrimSpace(string(line))
	}()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(5 * time.Second):
	}
	host, p, err := net.SplitHostPort(addr)
	n, pe := strconv.ParseUint(p, 10, 16)
	if host != "127.0.0.1" || err != nil || pe != nil || n == 0 {
		cancel()
		in.Close()
		cmd.Wait()
		return "", nil, errors.New("read host did not publish its endpoint")
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return addr, func() {
		in.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cancel()
			<-done
		}
		cancel()
	}, nil
}
