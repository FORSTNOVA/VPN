//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// execKernel is the kernel as a separate process, which is how Windows runs it.
// It is a process rather than a library because the kernel needs an
// administrator token to create its TUN adapter, and the window must not have
// to hold one; the child inherits only what it needs.
type execKernel struct {
	clashRS bool
	app     *app
	path    string
	cmd     *exec.Cmd
	exited  chan struct{}
	log     *os.File
	config  string
	managed *managedKernelProcess
}

// newKernel finds the kernel this copy will run: what the user chose, otherwise
// the copy that came with the package, otherwise the profile.
func (a *app) newKernel() (kernelRunner, error) {
	q := kernelAdmissionRequest{Op: "new"}
	e, w := newWindowsKernelAdmission(a, nil, q)
	r := runKernelAdmissionWithEffects(q, e)
	if r.Err != nil {
		return nil, r.Err
	}
	return w.runner, nil
}

func (k *execKernel) Running() bool {
	if k.managed != nil {
		select {
		case <-k.exited:
			return false
		default:
			return true
		}
	}
	r := k.app.runWindowsKernelAdmission(k, kernelAdmissionRequest{Op: "running"})
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Bool
}

func kernelArgs(clashRS bool, args ...string) []string {
	if clashRS {
		return append([]string{"--compatibility"}, args...)
	}
	return args
}

func (k *execKernel) Start() error {
	host, err := managedHostPath()
	if err != nil {
		return err
	}
	if host != "" {
		return k.startManaged(host)
	}
	return k.app.runWindowsKernelAdmission(k, kernelAdmissionRequest{Op: "start"}).Err
}

func (k *execKernel) Stop() error {
	if k.managed != nil {
		return k.stopManaged()
	}
	return k.app.runWindowsKernelAdmission(k, kernelAdmissionRequest{Op: "stop"}).Err
}

// A successful dial can belong to another program that bound the fixed port
// after our availability check. Only publish the proxy once its PID matches.
func (k *execKernel) verifyPortOwner(port int) error {
	return k.app.runWindowsKernelAdmission(k, kernelAdmissionRequest{Op: "owner", Port: port}).Err
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
	return a.runWindowsKernelAdmission(nil, kernelAdmissionRequest{Op: "check-config", Name: configPath}).Err
}

func (a *app) applyLiveConfig(payload string) error {
	if k, ok := a.kernel.(*execKernel); ok && k.managed != nil && k.managed.reloadKey != "" {
		m := k.managed
		ctx, cancel := context.WithTimeout(m.target.lifetime, 5*time.Second)
		defer cancel()
		b, err := json.Marshal(map[string]string{"payload": payload})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://"+m.target.addr+"/managed/config", bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+m.reloadKey)
		res, err := localMonitorClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("Mihomo returned HTTP %d", res.StatusCode)
		}
		return nil
	}
	_, err := a.mihomoRequest(http.MethodPut, "/configs", map[string]string{"payload": payload})
	return err
}
