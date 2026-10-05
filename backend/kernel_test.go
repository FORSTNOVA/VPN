package main

import "testing"

// fakeKernel is a kernel that is up without anything behind it. Most tests care
// about what the service does around a running kernel — the health sweep, the
// path monitor, the region switch — and not about the kernel itself, which the
// platform layer owns.
type fakeKernel struct {
	running bool
	// stopped counts the times the service ended it, so a test can tell "never
	// started" from "started and taken down again".
	stopped int
}

func (k *fakeKernel) Running() bool { return k.running }

func (k *fakeKernel) Start() error {
	k.running = true
	return nil
}

func (k *fakeKernel) Stop() error {
	k.running = false
	k.stopped++
	return nil
}

// runningKernel marks the app as connected.
func runningKernel(t *testing.T, a *app) {
	t.Helper()
	a.kernel = &fakeKernel{running: true}
}
