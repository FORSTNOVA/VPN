//go:build (windows && amd64 && (ruststore || rustcore)) || (android && cgo && rustcore && (amd64 || arm64))

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"

	"unicode/utf8"
	"unsafe"
)

const storeInputLimit = 8 << 20
const storeResultLimit = 16 << 20

var errStoreWire = errors.New("invalid Rust store reply")

type rustStoreError struct{ Category, SQLiteCode, Outcome uint32 }

func (e *rustStoreError) Error() string {
	return fmt.Sprintf("Rust store error category=%d sqlite=%d outcome=%d", e.Category, e.SQLiteCode, e.Outcome)
}

type rustStoreDLL struct {
	handle                                     nativeHandle
	open, claim, call, close, size, read, free uintptr
}

var storeDLLs = struct {
	sync.Mutex
	loaded map[string]*rustStoreDLL
}{loaded: map[string]*rustStoreDLL{}}

func loadRustStoreDLL(path string) *rustStoreDLL {
	if !filepath.IsAbs(path) {
		return nil
	}
	storeDLLs.Lock()
	defer storeDLLs.Unlock()
	if d := storeDLLs.loaded[path]; d != nil {
		return d
	}
	h, e := loadStoreLibrary(path)
	if e != nil {
		return nil
	}
	d := &rustStoreDLL{handle: h}
	complete := false
	defer func() {
		if !complete {
			_ = nativeClose(h)
		}
	}()
	version, e := nativeLookup(h, "smartvpn_store_version_v1")
	if e != nil {
		return nil
	}
	caps, e := nativeLookup(h, "smartvpn_store_capabilities_v1")
	if e != nil {
		return nil
	}
	v, _, _ := nativeCall(version)
	c, _, _ := nativeCall(caps)
	if v != 1 || c&0xffff != 0xffff {
		return nil
	}
	for name, dest := range map[string]*uintptr{"open": &d.open, "claim": &d.claim, "call": &d.call, "close": &d.close, "result_size": &d.size, "result_read": &d.read, "result_free": &d.free} {
		p, e := nativeLookup(h, "smartvpn_store_"+name+"_v1")
		if e != nil {
			return nil
		}
		*dest = p
	}
	complete = true
	storeDLLs.loaded[path] = d
	return d
}

// Selection can fall back ONLY before Rust open is attempted.
func selectStoreBackend(path string) (storeBackend, error) {
	if os.Getenv("SMARTVPN_STORE_BACKEND") == "go" {
		return openGoStoreBackend(path)
	}
	library := ""
	if runtime.GOOS == "windows" {
		if override, set := os.LookupEnv("SMARTVPN_RUST_STORE_DLL"); set {
			library = override
		} else if core := nativeCoreSelectedPath(os.Getenv); filepath.IsAbs(core) {
			library = filepath.Join(filepath.Dir(core), "smartvpn_store.dll")
		}
	} else if core := nativeCoreSelectedPath(os.Getenv); filepath.IsAbs(core) && packagedCoreLoaded() {
		library = filepath.Join(filepath.Dir(core), "libsmartvpn_store.so")
	}
	d := loadRustStoreDLL(library)
	if d == nil {
		return openGoStoreBackend(path)
	}
	return openRustStoreBackend(d, path)
}
func storeTransport(status uintptr) error {
	if int32(status) == 0 {
		return nil
	}
	return fmt.Errorf("Rust store transport status %d (operation must not be retried)", int32(status))
}

type storeReply struct {
	category, sqlcode, outcome uint32
	payload                    []byte
}

func (r storeReply) err() error {
	if r.category == 0 {
		return nil
	}
	return &rustStoreError{r.category, r.sqlcode, r.outcome}
}
func (d *rustStoreDLL) result(id uint64) (storeReply, error) {
	if id == 0 {
		return storeReply{}, errStoreWire
	}
	var size uintptr
	status, _, _ := nativeCall(d.size, uintptr(id), uintptr(unsafe.Pointer(&size)))
	if e := storeTransport(status); e != nil {
		return storeReply{}, e
	}
	if size < 20 || size > storeResultLimit {
		return storeReply{}, errStoreWire
	}
	buf := make([]byte, int(size))
	var written uintptr
	status, _, _ = nativeCall(d.read, uintptr(id), uintptr(unsafe.Pointer(&buf[0])), size, uintptr(unsafe.Pointer(&written)))
	runtime.KeepAlive(buf)
	if e := storeTransport(status); e != nil {
		return storeReply{}, e
	}
	if written != size {
		return storeReply{}, errStoreWire
	}
	return decodeRustStoreReply(buf)
}
func decodeRustStoreReply(buf []byte) (storeReply, error) {
	if len(buf) < 20 || len(buf) > storeResultLimit {
		return storeReply{}, errStoreWire
	}
	r := storeReply{binary.LittleEndian.Uint32(buf), binary.LittleEndian.Uint32(buf[4:]), binary.LittleEndian.Uint32(buf[8:]), buf[20:]}
	if r.category > 4 || r.outcome > 2 || binary.LittleEndian.Uint64(buf[12:]) != uint64(len(r.payload)) {
		return storeReply{}, errStoreWire
	}
	if r.category == 0 && (r.outcome != 1 || r.sqlcode != 0) || r.category != 0 && r.outcome == 1 {
		return storeReply{}, errStoreWire
	}
	return r, nil
}

func (d *rustStoreDLL) freeResult(id uint64) error {
	if id == 0 {
		return nil
	}
	status, _, _ := nativeCall(d.free, uintptr(id))
	return storeTransport(status)
}

type rustStoreBackend struct {
	mu     sync.Mutex
	dll    *rustStoreDLL
	handle uint64
}

var _ storeBackend = (*rustStoreBackend)(nil)

func openRustStoreBackend(d *rustStoreDLL, path string) (backend storeBackend, err error) {
	// Do not let UTF16 conversion replace an embedded NUL or invalid UTF8 path.
	if !utf8.ValidString(path) {
		return nil, errors.New("invalid UTF8 database path")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return nil, errors.New("invalid database path")
	}
	units := utf16.Encode([]rune(path))
	if len(units) == 0 || len(units) > 32767 {
		return nil, errors.New("invalid Rust database path")
	}

	var handle, result uint64
	status, _, _ := nativeCall(d.open, uintptr(unsafe.Pointer(&units[0])), uintptr(len(units)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(units)
	claimed := false
	defer func() {
		if !claimed {
			err = cleanupRustPendingResult(err, func() error { return d.freeResult(result) })
		}
	}()
	if e := storeTransport(status); e != nil {
		return nil, e
	}
	reply, e := d.result(result)
	if e != nil {
		return nil, e
	}
	if e = reply.err(); e != nil {
		return nil, e
	}
	if handle == 0 || len(reply.payload) != 0 {
		return nil, errStoreWire
	}
	status, _, _ = nativeCall(d.claim, uintptr(handle), uintptr(result))
	if e := storeTransport(status); e != nil {
		return nil, e
	}
	claimed = true
	return finishRustStoreOpen(&rustStoreBackend{dll: d, handle: handle}, func() error { return d.freeResult(result) })
}
func (s *rustStoreBackend) Close() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil
	}
	var result uint64
	status, _, _ := nativeCall(s.dll.close, uintptr(s.handle), uintptr(unsafe.Pointer(&result)))
	defer func() { err = cleanupRustPendingResult(err, func() error { return s.dll.freeResult(result) }) }()
	if e := storeTransport(status); e != nil {
		return e
	}
	reply, e := s.dll.result(result)
	if e != nil {
		return e
	}
	if e = reply.err(); e != nil {
		return e
	}
	if len(reply.payload) != 0 {
		return errStoreWire
	}
	s.handle = 0
	return nil
}
func (s *rustStoreBackend) execute(op uint32, w storeWriter) (reply storeReply, err error) {
	if w.err != nil {
		return reply, w.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return reply, errors.New("store is closed")
	}
	var input *byte
	if len(w.b) > 0 {
		input = &w.b[0]
	}
	var result uint64
	status, _, _ := nativeCall(s.dll.call, uintptr(s.handle), uintptr(op), uintptr(unsafe.Pointer(input)), uintptr(len(w.b)), uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(w.b)
	defer func() { err = cleanupRustPendingResult(err, func() error { return s.dll.freeResult(result) }) }()
	if e := storeTransport(status); e != nil {
		return reply, e
	}
	reply, err = s.dll.result(result)
	if err == nil {
		err = reply.err()
	}
	return
}
func (s *rustStoreBackend) write(op uint32, w storeWriter) error {
	r, e := s.execute(op, w)
	if e == nil && len(r.payload) != 0 {
		return errStoreWire
	}
	return e
}

type storeWriter struct {
	b   []byte
	err error
}

func (w *storeWriter) put(b []byte) {
	if w.err != nil {
		return
	}
	if len(b) > storeInputLimit-len(w.b) {
		w.err = errors.New("Rust store input limit exceeded")
		return
	}
	w.b = append(w.b, b...)
}
func (w *storeWriter) u(v uint64) { var b [8]byte; binary.LittleEndian.PutUint64(b[:], v); w.put(b[:]) }
func (w *storeWriter) i(v int64)  { w.u(uint64(v)) }
func (w *storeWriter) s(v string) { w.u(uint64(len(v))); w.put([]byte(v)) }
func (w *storeWriter) flag(v bool) {
	if v {
		w.put([]byte{1})
	} else {
		w.put([]byte{0})
	}
}
func (w *storeWriter) count(v int) {
	if v > 100000 {
		w.err = errors.New("Rust store item limit exceeded")
	}
	w.u(uint64(v))
}

type storeReader struct {
	b   []byte
	pos int
	err error
}

func (r *storeReader) take(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)-r.pos) {
		r.err = errStoreWire
		return nil
	}
	b := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b
}
func (r *storeReader) u() uint64 {
	b := r.take(8)
	if len(b) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}
func (r *storeReader) i() int64  { return int64(r.u()) }
func (r *storeReader) s() string { return string(r.take(r.u())) }
func (r *storeReader) flag() bool {
	b := r.take(1)
	if len(b) != 1 {
		return false
	}
	if b[0] > 1 {
		r.err = errStoreWire
	}
	return b[0] == 1
}
func (r *storeReader) count() int {
	n := r.u()
	if n > 100000 {
		r.err = errStoreWire
		return 0
	}
	return int(n)
}
func (r *storeReader) done() error {
	if r.err != nil {
		return r.err
	}
	if r.pos != len(r.b) {
		return errStoreWire
	}
	return nil
}

// A failed result cleanup after claim must not strand a newly owned store.
// On close failure the returned error retains the backend for explicit cleanup.
type rustCleanupOwner interface{ Close() error }

var rustStoreQuarantine struct {
	sync.Mutex
	owners []rustCleanupOwner
}

func quarantineRustStore(owner rustCleanupOwner) {
	rustStoreQuarantine.Lock()
	rustStoreQuarantine.owners = append(rustStoreQuarantine.owners, owner)
	rustStoreQuarantine.Unlock()
}

type rustStoreOpenCleanupError struct {
	owner rustCleanupOwner
	cause error
}

func (e *rustStoreOpenCleanupError) Error() string {
	return "Rust store cleanup failed; owner quarantined (process exit may be required): " + e.cause.Error()
}
func (e *rustStoreOpenCleanupError) Unwrap() error { return e.cause }
func (e *rustStoreOpenCleanupError) Close() error  { return e.owner.Close() }

// A pending result can only be freed. It can never claim/open/call a database.
type rustPendingResultCleanup struct {
	mu      sync.Mutex
	release func() error
	done    bool
}

func (p *rustPendingResultCleanup) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return nil
	}
	if err := p.release(); err != nil {
		return err
	}
	p.done = true
	return nil
}

type rustOpenCleanupGroup struct {
	backend storeBackend
	result  *rustPendingResultCleanup
}

func (g *rustOpenCleanupGroup) Close() error { return errors.Join(g.backend.Close(), g.result.Close()) }
func cleanupRustPendingResult(primary error, free func() error) error {
	p := &rustPendingResultCleanup{release: free}
	if e := p.Close(); e != nil {
		quarantineRustStore(p)
		return errors.Join(primary, &rustStoreOpenCleanupError{owner: p, cause: e})
	}
	return primary
}
func finishRustStoreOpen(b storeBackend, free func() error) (storeBackend, error) {
	p := &rustPendingResultCleanup{release: free}
	if err := p.Close(); err != nil {
		closeErr := b.Close()
		group := &rustOpenCleanupGroup{backend: b, result: p}
		quarantineRustStore(group)
		return nil, &rustStoreOpenCleanupError{owner: group, cause: errors.Join(err, closeErr)}
	}
	return b, nil
}
