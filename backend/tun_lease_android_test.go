//go:build android

package main

import (
	"os"
	"syscall"
	"testing"
)

func TestAndroidTunLeaseTransitions(t *testing.T) {
	var lease androidTunLease
	tests := []struct {
		op, fd  int
		allow   bool
		want    androidTunLease
		closeFD int
	}{
		{leaseGrant, 31, true, androidTunLease{1, 31, leaseAuthorized}, 0},
		{leaseHandOver, 0, false, androidTunLease{1, 31, leaseAuthorized | leaseKernelOwns}, 0},
		{leaseGrant, 31, false, androidTunLease{1, 31, leaseKernelOwns}, 0},
		{leaseGrant, 31, true, androidTunLease{1, 31, leaseAuthorized | leaseKernelOwns}, 0},
		{leaseGrant, 32, true, androidTunLease{2, 32, leaseAuthorized}, 0},
		{leaseRelease, 0, false, androidTunLease{3, 0, 0}, 32},
		{leaseRelease, 0, false, androidTunLease{4, 0, 0}, 0},
		{leaseGrant, 32, true, androidTunLease{5, 32, leaseAuthorized}, 0},
		{leaseHandOver, 0, false, androidTunLease{5, 32, leaseAuthorized | leaseKernelOwns}, 0},
		{leaseRelease, 0, false, androidTunLease{6, 0, 0}, 0},
	}
	for i, step := range tests {
		orphan, rust := lease.apply(step.op, int32(step.fd), step.allow)
		mode := os.Getenv("SMARTVPN_TEST_EXPECT_LEASE")
		if (mode == "rust" && !rust) || (mode == "go" && rust) {
			t.Fatalf("step %d: expected %s implementation", i, mode)
		}
		if lease != step.want || orphan != step.closeFD {
			t.Fatalf("step %d: state %+v, orphan %d", i, lease, orphan)
		}
	}
}

func TestAndroidTunLeaseReusedDescriptor(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var tun vpnTunnel
	tun.grant(true, fd, "old", nil, nil)
	old := tun.lease.generation
	if tun.handOver() != old || !tun.ownedBy(old) {
		t.Fatal("running kernel did not acquire its tunnel generation")
	}
	tun.release()
	if tun.ownedBy(old) {
		t.Fatal("released session remains eligible for hot reload")
	}
	// Use the same descriptor number for a second grant. It must still be
	// open, since the first release left it to the kernel owner.
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		t.Fatal(err)
	}
	tun.grant(true, fd, "new", nil, nil)
	if tun.ownedBy(old) || tun.ownedBy(tun.lease.generation) {
		t.Fatal("reused descriptor authorized a stale or unclaimed kernel")
	}
	tun.refreshReport(old, tunnelReport{Interface: "retired"})
	if _, _, name, _, _ := tun.state(); name != "new" {
		t.Fatal("retired report overwrote new tunnel")
	}
	tun.refreshReport(tun.lease.generation, tunnelReport{Interface: "current"})
	if _, _, name, _, _ := tun.state(); name != "current" {
		t.Fatal("current report was discarded")
	}
	tun.release()
	if err := syscall.Fstat(fd, &stat); err != syscall.EBADF {
		t.Fatalf("unclaimed descriptor survived release: %v", err)
	}
}
