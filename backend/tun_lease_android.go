//go:build android

package main

// Layout shared with android_tun_lease.rs. A repeated description of the
// current fd keeps its generation and ownership. Release retires the generation.
type androidTunLease struct {
	generation uint64
	fd         int32
	flags      uint32
}

const (
	leaseGrant = iota + 1
	leaseHandOver
	leaseRelease
)

const (
	leaseAuthorized = 1
	leaseKernelOwns = 2
)

func (l *androidTunLease) apply(op int, fd int32, authorized bool) (int, bool) {
	if orphan, used := l.applyRust(op, fd, authorized); used {
		return orphan, true
	}
	return l.applyGo(op, fd, authorized), false
}

func (l *androidTunLease) applyGo(op int, fd int32, authorized bool) int {
	switch op {
	case leaseGrant:
		if l.fd != fd {
			l.generation++
			l.flags = 0
		}
		l.fd = fd
		l.flags &= leaseKernelOwns
		if authorized {
			l.flags |= leaseAuthorized
		}
	case leaseHandOver:
		l.flags |= leaseKernelOwns
	case leaseRelease:
		var orphan int
		if l.flags&leaseKernelOwns == 0 && l.fd > 0 {
			orphan = int(l.fd)
		}
		*l = androidTunLease{generation: l.generation + 1}
		return orphan
	}
	return 0
}
