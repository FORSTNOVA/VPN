//go:build android && with_gvisor

package androidtun

import (
	"os"
	"testing"

	"github.com/metacubex/gvisor/pkg/buffer"
	"github.com/metacubex/gvisor/pkg/tcpip"
	"github.com/metacubex/gvisor/pkg/tcpip/stack"
	tun "github.com/metacubex/sing-tun"
	"golang.org/x/sys/unix"
)

type recordingEndpoint struct {
	stack.LinkEndpoint
	dispatcher stack.NetworkDispatcher
}

func (e *recordingEndpoint) Attach(d stack.NetworkDispatcher) { e.dispatcher = d }

func TestFilterForwardsDetach(t *testing.T) {
	ep := new(recordingEndpoint)
	filter := tun.LinkEndpointFilter{LinkEndpoint: ep}
	filter.Attach(&struct{ stack.NetworkDispatcher }{})
	if ep.dispatcher == nil {
		t.Fatal("missing dispatcher on attach")
	}
	filter.Attach(nil)
	if ep.dispatcher != nil {
		t.Fatal("receiver is still attached")
	}
}

func TestRetiredTunnelRejectsWrites(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	// Reserve a high slot so the replacement can acquire it without
	// overwriting an unrelated descriptor opened by the runtime.
	fd, err := unix.FcntlInt(w.Fd(), unix.F_DUPFD_CLOEXEC, 128)
	if err != nil {
		t.Fatal(err)
	}
	device, err := tun.New(tun.Options{FileDescriptor: fd, MTU: 1500})
	if err != nil {
		t.Fatal(err)
	}
	g := device.(tun.GVisorTun)
	ep, _, err := g.NewEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData([]byte{1})})
	defer pkt.DecRef()
	var batch stack.PacketBufferList
	batch.PushBack(pkt)
	if n, err := g.WritePacket(pkt); n != 1 || err != nil {
		t.Fatalf("active direct write: %d %v", n, err)
	}
	if n, err := ep.WritePackets(batch); n != 1 || err != nil {
		t.Fatalf("active batch write: %d %v", n, err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := unix.FcntlInt(sink.Fd(), unix.F_DUPFD_CLOEXEC, fd)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(replacement)
	if replacement != fd {
		t.Fatalf("descriptor reuse not exercised: %d != %d", replacement, fd)
	}
	if n, err := g.WritePacket(pkt); n != 0 || err == nil {
		t.Fatalf("retired direct write: %d %v", n, err)
	}
	n, batchErr := ep.WritePackets(batch)
	if _, closed := batchErr.(*tcpip.ErrClosedForSend); !closed || n != 0 {
		t.Fatalf("retired batch write: %d %v", n, batchErr)
	}
}
