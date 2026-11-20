// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// powerCutFS is a filesys.FS that models what a disk keeps through a power
// cut. It passes every call to its base and records, per file, the content a
// returned Sync made durable and every write since. cut writes the image the
// disk would hold had power failed during one file's in-flight write: every
// file keeps its synced content and drops its unsynced writes, except the
// file being written, which also keeps its earlier unsynced writes in order
// and only a seeded random byte prefix of the in-flight write, so a block
// really tears. Directory entries are treated as durable.
type powerCutFS struct {
	filesys.FS
	root string

	mu    sync.Mutex
	files map[string]*pcState

	// hook, when set, runs after each recorded write, outside mu, with the
	// file's name and the number of Syncs that file has returned.
	hook func(name string, syncs int)
}

type pcWrite struct {
	off  int64
	data []byte
}

type pcState struct {
	durable []byte
	cur     []byte
	pending []pcWrite
	syncs   int
}

func newPowerCutFS(base filesys.FS, root string) *powerCutFS {
	return &powerCutFS{FS: base, root: root, files: map[string]*pcState{}}
}

// OnHost reports true: the base is the host file system.
func (p *powerCutFS) OnHost() bool { return true }

func applyWrite(buf []byte, w pcWrite) []byte {
	end := w.off + int64(len(w.data))
	if int64(len(buf)) < end {
		buf = append(buf, make([]byte, end-int64(len(buf)))...)
	}
	copy(buf[w.off:end], w.data)
	return buf
}

func (p *powerCutFS) Create(name string) (filesys.File, error) {
	f, err := p.FS.Create(name)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.files[name] = &pcState{}
	p.mu.Unlock()
	return &pcFile{File: f, fs: p, name: name}, nil
}

func (p *powerCutFS) OpenReadWrite(name string) (filesys.File, error) {
	f, err := p.FS.OpenReadWrite(name)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if _, ok := p.files[name]; !ok {
		data, rerr := os.ReadFile(name) // #nosec G304 -- test file under t.TempDir
		if rerr != nil {
			p.mu.Unlock()
			_ = f.Close()
			return nil, rerr
		}
		p.files[name] = &pcState{durable: data, cur: bytes.Clone(data), syncs: 1}
	}
	p.mu.Unlock()
	return &pcFile{File: f, fs: p, name: name}, nil
}

func (p *powerCutFS) Rename(oldname, newname string) error {
	if err := p.FS.Rename(oldname, newname); err != nil {
		return err
	}
	p.mu.Lock()
	if st, ok := p.files[oldname]; ok {
		p.files[newname] = st
		delete(p.files, oldname)
	}
	p.mu.Unlock()
	return nil
}

func (p *powerCutFS) Link(oldname, newname string) error {
	if err := p.FS.Link(oldname, newname); err != nil {
		return err
	}
	p.mu.Lock()
	if st, ok := p.files[oldname]; ok {
		p.files[newname] = &pcState{durable: bytes.Clone(st.durable), cur: bytes.Clone(st.cur), syncs: st.syncs}
	}
	p.mu.Unlock()
	return nil
}

func (p *powerCutFS) Remove(name string) error {
	if err := p.FS.Remove(name); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.files, name)
	p.mu.Unlock()
	return nil
}

func (p *powerCutFS) RemoveAll(name string) error {
	if err := p.FS.RemoveAll(name); err != nil {
		return err
	}
	p.mu.Lock()
	for n := range p.files {
		if n == name || strings.HasPrefix(n, name+string(filepath.Separator)) {
			delete(p.files, n)
		}
	}
	p.mu.Unlock()
	return nil
}

func (p *powerCutFS) record(name string, off int64, data []byte) {
	p.mu.Lock()
	st, ok := p.files[name]
	syncs := 0
	if ok {
		w := pcWrite{off: off, data: bytes.Clone(data)}
		st.cur = applyWrite(st.cur, w)
		st.pending = append(st.pending, w)
		syncs = st.syncs
	}
	hook := p.hook
	p.mu.Unlock()
	if ok && hook != nil {
		hook(name, syncs)
	}
}

// writes reports how many writes name has had since its last Sync.
func (p *powerCutFS) writes(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.files[name]; ok {
		return len(st.pending)
	}
	return 0
}

func (p *powerCutFS) synced(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.files[name]; ok {
		st.durable = bytes.Clone(st.cur)
		st.pending = nil
		st.syncs++
	}
}

// cut writes under dst the image a power cut during inflight's last write
// leaves on disk; seed picks how much of that write reached the platter.
func (p *powerCutFS) cut(dst string, seed uint64, inflight string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	for name, st := range p.files {
		img := bytes.Clone(st.durable)
		if name == inflight && len(st.pending) > 0 {
			last := len(st.pending) - 1
			for _, w := range st.pending[:last] {
				img = applyWrite(img, w)
			}
			w := st.pending[last]
			if n := len(w.data); n > 0 {
				img = applyWrite(img, pcWrite{off: w.off, data: w.data[:rng.IntN(n)]})
			}
		}
		rel, err := filepath.Rel(p.root, name)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(out, img, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pcFile records the writes and Syncs made through it.
type pcFile struct {
	filesys.File
	fs   *powerCutFS
	name string
	pos  int64
}

func (f *pcFile) Write(b []byte) (int, error) {
	n, err := f.File.Write(b)
	if n > 0 {
		f.fs.record(f.name, f.pos, b[:n])
		f.pos += int64(n)
	}
	return n, err
}

func (f *pcFile) WriteAt(b []byte, off int64) (int, error) {
	n, err := f.File.WriteAt(b, off)
	if n > 0 {
		f.fs.record(f.name, off, b[:n])
	}
	return n, err
}

func (f *pcFile) Sync() error {
	if err := f.File.Sync(); err != nil {
		return err
	}
	f.fs.synced(f.name)
	return nil
}

// powerCutRecord returns a record of a size that sometimes stays inside the
// trailing block and sometimes crosses one or more block boundaries.
func powerCutRecord(rng *rand.Rand) []byte {
	var n int
	switch rng.IntN(4) {
	case 0:
		n = 1 + rng.IntN(16)
	case 1, 2:
		n = 1 + rng.IntN(BlockSize/2)
	default:
		n = BlockSize + rng.IntN(2*BlockSize)
	}
	rec := make([]byte, n)
	for i := range rec {
		rec[i] = byte(rng.Uint32())
	}
	return rec
}

// TestPowerCut_EverySyncedRecordSurvives appends records to a WAL-like and a
// MANIFEST-like file, Syncing after each, then cuts power during a later
// write. The reopened file must open, hold every byte a returned Sync
// acknowledged, and hold nothing that was not written.
func TestPowerCut_EverySyncedRecordSurvives(t *testing.T) {
	b := newFakeBarrier(t)
	for _, name := range []string{"000002.log", "MANIFEST-000001"} {
		for seed := uint64(0); seed < 48; seed++ {
			t.Run(fmt.Sprintf("%s/seed=%d", name, seed), func(t *testing.T) {
				powerCutFileRun(t, b, name, seed)
			})
		}
	}
}

func powerCutFileRun(t *testing.T, b *fakeBarrier, name string, seed uint64) {
	src, dst := t.TempDir(), t.TempDir()
	pc := newPowerCutFS(filesys.Default, src)
	efs := New(pc, b)
	path := filepath.Join(src, name)
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewPCG(seed, 7))

	f, err := efs.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var written, acked []byte
	appendRec := func() {
		rec := powerCutRecord(rng)
		if _, err := f.Write(rec); err != nil {
			t.Fatalf("Write: %v", err)
		}
		written = append(written, rec...)
	}
	for i, n := 0, 1+rng.IntN(10); i < n; i++ {
		appendRec()
		if err := f.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		acked = bytes.Clone(written)
	}

	skip := rng.IntN(3)
	cutDone := false
	pc.hook = func(n string, _ int) {
		if cutDone || n != path {
			return
		}
		if skip > 0 {
			skip--
			return
		}
		if err := pc.cut(dst, seed, n); err != nil {
			t.Errorf("cut: %v", err)
		}
		cutDone = true
	}
	for i := 0; !cutDone && i < 64; i++ {
		appendRec()
		if err := f.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if !cutDone {
			acked = bytes.Clone(written)
		}
	}
	if !cutDone {
		t.Fatal("power cut never fired")
	}
	pc.hook = nil
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	g, err := New(filesys.Default, b).Open(filepath.Join(dst, name))
	if err != nil {
		t.Fatalf("open after power cut: %v", err)
	}
	defer func() { _ = g.Close() }()
	got, err := io.ReadAll(g)
	if err != nil {
		t.Fatalf("read after power cut: %v", err)
	}
	if len(got) < len(acked) || !bytes.Equal(got[:len(acked)], acked) {
		t.Fatalf("synced bytes lost: read %d bytes, %d acknowledged", len(got), len(acked))
	}
	if !bytes.HasPrefix(written, got) {
		t.Fatalf("read %d bytes that were never written in that order", len(got))
	}
}

// TestPowerCut_FixedLayoutLogTearOpensOrRefuses covers a WAL in the fixed-size
// layout, written before the framed layout existed (a device's preserved
// WAL after a qrdb bump). That layout rewrites the trailing block in place
// on every Sync, so the tear itself destroys that block's previous content;
// the file must still open, end at the torn block, and replay every block
// before it.
func TestPowerCut_FixedLayoutLogTearOpensOrRefuses(t *testing.T) {
	b := newFakeBarrier(t)
	for seed := uint64(0); seed < 48; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			pc := newPowerCutFS(filesys.Default, src)
			path := filepath.Join(src, "000002.log")
			// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
			rng := rand.New(rand.NewPCG(seed, 11))
			base, err := pc.Create(path)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			h, err := newHeader(b) // no framed flag: the fixed-size layout
			if err != nil {
				t.Fatalf("newHeader: %v", err)
			}
			f, err := newWriteFile(base, h)
			if err != nil {
				t.Fatalf("newWriteFile: %v", err)
			}
			var written, acked []byte
			cutDone := false
			for i := 0; !cutDone && i < 64; i++ {
				if i == 1+rng.IntN(6) || i == 8 {
					pc.hook = func(n string, _ int) {
						if !cutDone {
							if err := pc.cut(dst, seed, n); err != nil {
								t.Errorf("cut: %v", err)
							}
							cutDone = true
						}
					}
				}
				rec := powerCutRecord(rng)
				if _, err := f.Write(rec); err != nil {
					t.Fatalf("Write: %v", err)
				}
				written = append(written, rec...)
				if err := f.Sync(); err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if !cutDone {
					acked = bytes.Clone(written)
				}
			}
			pc.hook = nil
			_ = f.Close()

			// The fixed layout records no durable boundary, so a final
			// block the tear left unauthentic refuses the open rather
			// than open the log short; any other tear opens.
			g, err := New(filesys.Default, b).Open(filepath.Join(dst, "000002.log"))
			if errors.Is(err, ErrAuthFailed) {
				return
			}
			if err != nil {
				t.Fatalf("open fixed-layout WAL after power cut: %v", err)
			}
			defer func() { _ = g.Close() }()
			got, err := io.ReadAll(g)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.HasPrefix(written, got) {
				t.Fatalf("read %d bytes that were never written in that order", len(got))
			}
			if whole := len(acked) / BlockSize * BlockSize; len(got) < whole {
				t.Fatalf("blocks before the torn one lost: read %d bytes, %d in whole synced blocks", len(got), whole)
			}
		})
	}
}

// TestFramed_TamperIsNotATear flips a byte in a frame that has frames after
// it: that is tamper or corruption, not a power cut, and the open must
// refuse with ErrAuthFailed rather than end the log early.
func TestFramed_TamperIsNotATear(t *testing.T) {
	b := newFakeBarrier(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "000002.log")
	efs := New(filesys.Default, b)
	f, err := efs.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.Write(bytes.Repeat([]byte{byte('a' + i)}, 100)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := f.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	ef, ok := f.(*encryptedFile)
	if !ok || !ef.framed || len(ef.frames) != 3 {
		t.Fatalf("want a framed file with 3 frames, got %T", f)
	}
	second := ef.frames[1].phys
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	for _, flip := range []int64{second + frameLenSize + 20, second} {
		bad := bytes.Clone(raw)
		bad[flip] ^= 0x01
		// #nosec G703 -- path names a file under t.TempDir() that this test created; no component comes from outside the test.
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := efs.Open(path); !errors.Is(err, ErrAuthFailed) {
			t.Errorf("open with byte %d flipped: err = %v, want ErrAuthFailed", flip, err)
		}
	}

	// Swapping two equal-length frames must not authenticate either.
	swapped := bytes.Clone(raw)
	a, c := ef.frames[0], ef.frames[1]
	size := int64(frameLenSize + BlockOverhead + a.n)
	copy(swapped[a.phys:a.phys+size], raw[c.phys:c.phys+size])
	copy(swapped[c.phys:c.phys+size], raw[a.phys:a.phys+size])
	// #nosec G703 -- path names a file under t.TempDir() that this test created; no component comes from outside the test.
	if err := os.WriteFile(path, swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := efs.Open(path); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("open with frames swapped: err = %v, want ErrAuthFailed", err)
	}
}

// TestFramed_ReopenAppendsAfterTornTail reopens a framed log whose final
// frame is torn, appends, and checks the log reads back as the synced
// prefix followed by the new bytes.
func TestFramed_ReopenAppendsAfterTornTail(t *testing.T) {
	b := newFakeBarrier(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "MANIFEST-000003")
	efs := New(filesys.Default, b)
	f, err := efs.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := bytes.Repeat([]byte{'x'}, BlockSize+300)
	if _, err := f.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// A torn frame of garbage after the last authentic one.
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	torn := append(bytes.Clone(raw), 0x40, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef)
	// #nosec G703 -- path names a file under t.TempDir() that this test created; no component comes from outside the test.
	if err := os.WriteFile(path, torn, 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := efs.OpenReadWrite(path)
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	more := []byte("appended after reopen")
	if _, err := g.Write(more); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	h, err := efs.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = h.Close() }()
	got, err := io.ReadAll(h)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(bytes.Clone(first), more...); !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d", len(got), len(want))
	}
}

// TestPowerCut_PebbleKeepsEveryAckedKey runs Pebble over encfs with
// pebble.Sync writes and flushes, cuts power during a write to the WAL or
// the MANIFEST, and reopens the image: pebble.Open must succeed and every
// key whose Set returned must read back.
func TestPowerCut_PebbleKeepsEveryAckedKey(t *testing.T) {
	for _, target := range []string{"wal", "manifest", "wal-header", "manifest-header"} {
		for seed := uint64(0); seed < 6; seed++ {
			t.Run(fmt.Sprintf("%s/seed=%d", target, seed), func(t *testing.T) {
				powerCutPebbleRun(t, target, seed)
			})
		}
	}
}

func powerCutTargets(target, name string) bool {
	base := filepath.Base(name)
	if strings.HasPrefix(target, "wal") {
		return strings.HasSuffix(base, ".log")
	}
	return strings.HasPrefix(base, "MANIFEST-")
}

func powerCutKey(i int) []byte { return []byte(fmt.Sprintf("key-%06d", i)) }

func powerCutValue(i int) []byte {
	return bytes.Repeat([]byte{byte('a' + i%26)}, 100+(i*373)%3000)
}

func powerCutPebbleRun(t *testing.T, target string, seed uint64) {
	b := newFakeBarrier(t)
	src, dst := t.TempDir(), t.TempDir()
	var pc *powerCutFS
	opts := &pebble.Options{FS: pebblestore.WrapVFS(vfs.Default, func(base filesys.FS) filesys.FS {
		pc = newPowerCutFS(base, src)
		return New(pc, b)
	})}

	var (
		acked    atomic.Int64
		armed    atomic.Bool
		done     atomic.Bool
		skip     atomic.Int64
		cutAcked atomic.Int64
		cutErr   atomic.Pointer[error]
	)
	skip.Store(int64(seed % 3))
	pc.hook = func(name string, syncs int) {
		if !armed.Load() || done.Load() || !powerCutTargets(target, name) {
			return
		}
		if strings.HasSuffix(target, "-header") {
			// Cut while the fresh file's header is its only write.
			if syncs != 0 || pc.writes(name) != 1 {
				return
			}
		} else if syncs == 0 {
			return
		}
		if skip.Add(-1) >= 0 {
			return
		}
		if !done.CompareAndSwap(false, true) {
			return
		}
		a := acked.Load()
		if err := pc.cut(dst, seed, name); err != nil {
			cutErr.Store(&err)
		}
		cutAcked.Store(a)
	}

	if target == "manifest-header" {
		// Rotate the MANIFEST on every version edit, so fresh ones are
		// created (and their headers written) while keys are acked.
		opts.MaxManifestFileSize = 1
	}
	db, err := pebble.Open(src, opts)
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	armAt := 3 + int(seed)
	for i := 0; i < 400 && !done.Load(); i++ {
		if i == armAt {
			armed.Store(true)
		}
		if err := db.Set(powerCutKey(i), powerCutValue(i), pebble.Sync); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
		acked.Store(int64(i + 1))
		if i%5 == 4 {
			if err := db.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !done.Load() {
		t.Fatal("power cut never fired")
	}
	if p := cutErr.Load(); p != nil {
		t.Fatalf("cut: %v", *p)
	}

	ropts := &pebble.Options{FS: pebblestore.WrapVFS(vfs.Default, func(base filesys.FS) filesys.FS {
		return New(base, b)
	})}
	db2, err := pebble.Open(dst, ropts)
	if err != nil {
		t.Fatalf("pebble.Open after power cut: %v", err)
	}
	defer func() { _ = db2.Close() }()
	n := int(cutAcked.Load())
	for i := 0; i < n; i++ {
		got, closer, err := db2.Get(powerCutKey(i))
		if err != nil {
			t.Fatalf("acknowledged key %d of %d lost: %v", i, n, err)
		}
		ok := bytes.Equal(got, powerCutValue(i))
		_ = closer.Close()
		if !ok {
			t.Fatalf("acknowledged key %d has a wrong value", i)
		}
	}
}

// freshHeaderBytes returns the on-disk header encfs writes for a new file
// called name: the bytes a power cut can tear before its first Sync.
func freshHeaderBytes(t *testing.T, b barrierKEK, name string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := New(filesys.Default, b).Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	img, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	if len(img) < fixedHeaderSize {
		t.Fatalf("header is %d bytes, under the fixed %d", len(img), fixedHeaderSize)
	}
	// A framed file's commit slots follow the header: return the header only.
	return img[:int(img[offHeaderSize])|int(img[offHeaderSize+1])<<8]
}

// pendingHeaderBytes is freshHeaderBytes as a crash inside Create leaves it:
// the first magic byte still carries the pending marker.
func pendingHeaderBytes(t *testing.T, b barrierKEK, name string) []byte {
	t.Helper()
	hdr := bytes.Clone(freshHeaderBytes(t, b, name))
	hdr[offMagic] = pendingMagic
	return hdr
}

// TestPowerCut_FreshFramedHeaderTornOpensEmpty cuts a fresh WAL and a fresh
// MANIFEST inside their header, at every byte offset. No record can have
// been acknowledged, so both opens yield an empty file, and a read-write
// open takes appends that survive a reopen.
func TestPowerCut_FreshFramedHeaderTornOpensEmpty(t *testing.T) {
	b := newFakeBarrier(t)
	for _, name := range []string{"000007.log", "MANIFEST-000003"} {
		hdr := pendingHeaderBytes(t, b, name)
		for cut := 0; cut <= len(hdr); cut++ {
			t.Run(fmt.Sprintf("%s/cut=%d", name, cut), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, hdr[:cut], 0o600); err != nil {
					t.Fatalf("write torn header: %v", err)
				}
				fsys := New(filesys.Default, b)
				r, err := fsys.Open(path)
				if err != nil {
					t.Fatalf("Open torn header: %v", err)
				}
				fi, err := r.Stat()
				if err != nil {
					t.Fatalf("Stat: %v", err)
				}
				got, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil {
					t.Fatalf("read: %v", err)
				}
				if fi.Size() != 0 || len(got) != 0 {
					t.Fatalf("torn header opened with size %d, read %d bytes; want empty", fi.Size(), len(got))
				}

				w, err := fsys.OpenReadWrite(path)
				if err != nil {
					t.Fatalf("OpenReadWrite torn header: %v", err)
				}
				if fi, err := w.Stat(); err != nil || fi.Size() != 0 {
					t.Fatalf("read-write open of torn header: size %v err %v; want 0", fi, err)
				}
				rec := []byte("first record after the cut")
				if _, err := w.Write(rec); err != nil {
					t.Fatalf("Write: %v", err)
				}
				if err := w.Sync(); err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if err := w.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				r2, err := fsys.Open(path)
				if err != nil {
					t.Fatalf("reopen: %v", err)
				}
				defer func() { _ = r2.Close() }()
				got, err = io.ReadAll(r2)
				if err != nil {
					t.Fatalf("read after reopen: %v", err)
				}
				if !bytes.Equal(got, rec) {
					t.Fatalf("after reopen read %q, want %q", got, rec)
				}
			})
		}
	}
}

// TestPowerCut_HeaderRefusalsStay keeps the typed refusals the torn-header
// rule must not swallow: a complete framed header that fails to
// authenticate, and a short header on a file that is not an append log.
func TestPowerCut_HeaderRefusalsStay(t *testing.T) {
	b := newFakeBarrier(t)
	fsys := New(filesys.Default, b)
	open := func(t *testing.T, path string) (error, error) {
		t.Helper()
		var errs [2]error
		for i, o := range []func(string) (filesys.File, error){fsys.Open, fsys.OpenReadWrite} {
			f, err := o(path)
			if err == nil {
				_ = f.Close()
			}
			errs[i] = err
		}
		return errs[0], errs[1]
	}

	t.Run("framed/corrupt-dek", func(t *testing.T) {
		hdr := bytes.Clone(freshHeaderBytes(t, b, "000009.log"))
		hdr[len(hdr)-1] ^= 0x5a
		path := filepath.Join(t.TempDir(), "000009.log")
		if err := os.WriteFile(path, hdr, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		ro, rw := open(t, path)
		if ro == nil || rw == nil {
			t.Fatalf("corrupt framed header opened: Open %v, OpenReadWrite %v", ro, rw)
		}
	})
	t.Run("framed/bad-version", func(t *testing.T) {
		hdr := bytes.Clone(freshHeaderBytes(t, b, "MANIFEST-000009"))
		hdr[offVersion]++
		path := filepath.Join(t.TempDir(), "MANIFEST-000009")
		if err := os.WriteFile(path, hdr, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		ro, rw := open(t, path)
		if !errors.Is(ro, ErrUnsupportedVersion) || !errors.Is(rw, ErrUnsupportedVersion) {
			t.Fatalf("bad-version framed header: Open %v, OpenReadWrite %v; want ErrUnsupportedVersion", ro, rw)
		}
	})
	t.Run("sstable/short", func(t *testing.T) {
		hdr := freshHeaderBytes(t, b, "000011.sst")
		for cut := fixedHeaderSize; cut < len(hdr); cut++ {
			path := filepath.Join(t.TempDir(), "000011.sst")
			if err := os.WriteFile(path, hdr[:cut], 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			ro, rw := open(t, path)
			if !errors.Is(ro, ErrTruncatedHeader) || !errors.Is(rw, ErrTruncatedHeader) {
				t.Fatalf("cut=%d short sstable header: Open %v, OpenReadWrite %v; want ErrTruncatedHeader", cut, ro, rw)
			}
		}
	})
}

// TestFramed_CorruptHeaderSizeKeepsFile: a WAL or MANIFEST holding a synced
// record whose header size field (and the wrapped-DEK length beside it) is
// rewritten to a larger in-bounds value reads as a header the file ends
// inside. That is corruption, not a tear: a tear leaves a prefix of the real
// header, whose size is the fresh header's size. Both opens refuse, and the
// bytes on disk are unchanged.
func TestFramed_CorruptHeaderSizeKeepsFile(t *testing.T) {
	b := newFakeBarrier(t)
	fsys := New(filesys.Default, b)
	for _, name := range []string{"000013.log", "MANIFEST-000013"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			w, err := fsys.Create(path)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if _, err := w.Write([]byte("one synced record")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := w.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			img, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			size := len(img) + 1
			if size > maxHeaderSize {
				t.Fatalf("file is %d bytes; the test needs it under the %d-byte header bound", len(img), maxHeaderSize)
			}
			binary.LittleEndian.PutUint16(img[offHeaderSize:], uint16(size))
			binary.LittleEndian.PutUint32(img[offWrappedDEKLen:], uint32(size-fixedHeaderSize))
			if _, err := readHeader(bytes.NewReader(img), b); !errors.Is(err, ErrTruncatedHeader) {
				t.Fatalf("corrupted header reads as %v; the test needs ErrTruncatedHeader", err)
			}
			// #nosec G703 -- path names a file under t.TempDir() that this test created; no component comes from outside the test.
			if err := os.WriteFile(path, img, 0o600); err != nil {
				t.Fatalf("write corrupted header: %v", err)
			}
			for i, open := range []func(string) (filesys.File, error){fsys.Open, fsys.OpenReadWrite} {
				f, err := open(path)
				if err == nil {
					_ = f.Close()
					t.Fatalf("open %d of a corrupted header size succeeded; want a refusal", i)
				}
				got, rerr := os.ReadFile(path)
				if rerr != nil {
					t.Fatalf("read after open %d: %v", i, rerr)
				}
				if !bytes.Equal(got, img) {
					t.Fatalf("open %d (err %v) changed the file on disk", i, err)
				}
			}
		})
	}
}
