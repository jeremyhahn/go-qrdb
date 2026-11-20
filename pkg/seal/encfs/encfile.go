// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// ErrWriteAtUnsupported is returned by WriteAt when the offset does
// not match the file's current logical end. The encfs V1 supports
// only append-style writes; Pebble does not perform mid-file rewrites
// in its standard I/O path. Should this restriction prove too tight
// the package can be extended with read-modify-write block handling.
var ErrWriteAtUnsupported = errors.New("encfs: WriteAt at non-append offset is not supported")

// ErrFileClosed is returned by file operations after Close.
var ErrFileClosed = errors.New("encfs: file is closed")

// ErrReadOnly is returned by write methods on a read-only file.
var ErrReadOnly = errors.New("encfs: file is read-only")

// encryptedFile implements filesys.File on top of an underlying base file
// holding encfs-formatted ciphertext. One instance corresponds to one
// open Pebble file handle. The struct is small enough to embed in the
// returned filesys.File interface value without additional indirection.
type encryptedFile struct {
	base       filesys.File
	crypto     *blockCrypto
	headerSize int64
	readOnly   bool
	framed     bool // framed, seal-on-sync layout (see the file comment above writeFrameLocked)

	mu          sync.Mutex
	closed      bool
	logicalSize int64      // total logical bytes (header excluded)
	flushedSize int64      // logical bytes whose content is on disk; <= logicalSize
	seqPos      int64      // logical position for sequential Read
	tail        []byte     // bytes after flushedSize that may still grow before next full-block flush
	frames      []frameRef // framed layout: every frame on disk, in order
	physEnd     int64      // framed layout: physical offset the next frame is written at
	commitSeq   int64      // framed layout: seq of the newest commit slot
	durable     int64      // framed layout: frames the newest commit slot records as synced
}

// frameRef locates one frame of a framed file.
type frameRef struct {
	phys    int64 // physical offset of the frame's length prefix
	logical int64 // logical offset of the frame's first plaintext byte
	n       int   // plaintext length
}

// newWriteFile wraps a freshly-created base file in an encryptedFile
// configured for sequential writes. The base file MUST be empty; the
// header is written immediately.
func newWriteFile(base filesys.File, h *header) (*encryptedFile, error) {
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	f := &encryptedFile{
		base:       base,
		crypto:     crypto,
		headerSize: int64(h.totalSize()),
		framed:     h.framed(),
	}
	hdr := h.marshal()
	if f.framed {
		// A framed log starts with its commit slots, synced with the
		// header so a later open can tell a crash inside Create (slots
		// missing, no frames) from a corrupted log.
		f.headerSize = framedDataStart(h)
		f.commitSeq = commitSlots - 1
		// The real magic is stamped only after both are synced, so a
		// short stamped file is a truncated log, not a torn Create.
		if err := writeFramedStart(base, append(hdr, crypto.freshCommitSlots(f.headerSize)...)); err != nil {
			return nil, err
		}
	} else if _, err := base.Write(hdr); err != nil {
		return nil, err
	}
	f.physEnd = f.headerSize
	return f, nil
}

// newReadFile wraps an existing base file in an encryptedFile
// configured for read-only access. The header has already been parsed
// and the base read cursor sits immediately past the header.
func newReadFile(base filesys.File, h *header, physicalSize int64, appendLog bool) (*encryptedFile, error) {
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	f := &encryptedFile{
		base:       base,
		crypto:     crypto,
		headerSize: int64(h.totalSize()),
		readOnly:   true,
		framed:     h.framed(),
	}
	payload := physicalSize - f.headerSize
	switch {
	case f.framed:
		if payload < 0 {
			return nil, &NegativePayloadLengthError{Length: payload}
		}
		f.headerSize = framedDataStart(h)
		if err := f.scanFrames(physicalSize); err != nil {
			return nil, err
		}
		return f, nil
	case appendLog:
		f.logicalSize, err = f.fixedLogLogicalSize(payload)
	default:
		f.logicalSize, err = physicalToLogicalSize(payload)
	}
	if err != nil {
		return nil, err
	}
	f.flushedSize = f.logicalSize
	return f, nil
}

// scanFrames indexes and authenticates every frame of a framed file of
// physicalSize bytes. A torn, short or unauthenticated FINAL frame is the
// end of the log: a framed file never rewrites a frame a Sync made durable,
// so the final frame can only hold bytes no Sync acknowledged. A frame that
// fails authentication with bytes after it (other than zero fill) is tamper
// or corruption, not a tear, and refuses the open with ErrAuthFailed.
func (f *encryptedFile) scanFrames(physicalSize int64) error {
	start := f.headerSize
	f.physEnd = start
	if physicalSize < start {
		// The stamped magic proves Create synced the header with both
		// commit slots (a crash inside Create leaves the pending marker,
		// which tornFramedHeader recovers), so a file that ends inside
		// the slots lost acknowledged bytes: refuse it, never open empty.
		return fmt.Errorf("%w: framed log ends at %d inside its commit slots (data starts at %d)", ErrTruncatedHeader, physicalSize, start)
	}
	rec, ok, err := f.readCommit(physicalSize)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: framed log has no authentic commit slot", ErrAuthFailed)
	}
	f.commitSeq, f.durable = rec.seq, rec.count
	if rec.count == 0 && rec.end != start {
		return fmt.Errorf("%w: commit slot ends at %d with no frames", ErrAuthFailed, rec.end)
	}
	pos := start
	var logical int64
	for pos < physicalSize {
		idx := int64(len(f.frames))
		durable := idx < rec.count
		frame, size, ok, err := f.scanFrame(pos, physicalSize, idx)
		if err != nil {
			return err
		}
		if !ok {
			if durable {
				return fmt.Errorf("%w: synced frame %d at offset %d", ErrAuthFailed, idx, pos)
			}
			break // torn or unsealed bytes past the last Sync
		}
		if idx == rec.count-1 && pos+size != rec.end {
			return fmt.Errorf("%w: synced frame %d ends at %d, commit slot says %d", ErrAuthFailed, idx, pos+size, rec.end)
		}
		frame.logical = logical
		f.frames = append(f.frames, frame)
		logical += int64(frame.n)
		pos += size
	}
	if int64(len(f.frames)) < rec.count {
		return fmt.Errorf("%w: %d synced frames, %d on disk", ErrAuthFailed, rec.count, len(f.frames))
	}
	f.logicalSize = logical
	f.flushedSize = logical
	f.physEnd = pos
	return nil
}

// scanFrame reads and authenticates the frame at pos as frame idx. ok is
// false when the bytes there are not a whole authentic frame (a length
// prefix cut short, out of range, running past the file, or a seal that
// does not open); err is an I/O error only.
func (f *encryptedFile) scanFrame(pos, physicalSize, idx int64) (frameRef, int64, bool, error) {
	rem := physicalSize - pos
	if rem < frameLenSize {
		return frameRef{}, 0, false, nil
	}
	var lb [frameLenSize]byte
	if _, err := f.base.ReadAt(lb[:], pos); err != nil {
		return frameRef{}, 0, false, err
	}
	n := int(binary.LittleEndian.Uint32(lb[:]))
	if n <= 0 || n > BlockSize {
		return frameRef{}, 0, false, nil
	}
	size := int64(frameLenSize + BlockOverhead + n)
	if size > rem {
		return frameRef{}, 0, false, nil
	}
	sealed := make([]byte, BlockOverhead+n)
	if _, err := f.base.ReadAt(sealed, pos+frameLenSize); err != nil {
		return frameRef{}, 0, false, err
	}
	if _, err := f.crypto.openFrame(sealed, idx, n); err != nil {
		return frameRef{}, 0, false, nil
	}
	return frameRef{phys: pos, n: n}, size, true, nil
}

// commitRecord is the content of a commit slot: the durable boundary of a
// framed log. Frames [0, count) end at physical offset end and were synced;
// only bytes past end may be torn.
type commitRecord struct {
	seq   int64 // increments on every commit; slot seq%commitSlots holds it
	count int64
	end   int64
}

const (
	// commitSlots is the number of alternating commit slots between a
	// framed header and its first frame. A commit overwrites the older
	// slot, so a commit torn by a power cut leaves the other one whole.
	commitSlots = 2
	// commitPlainSize is the plaintext of a commit slot: seq, count, end.
	commitPlainSize = 24
	// commitSlotSize is the on-disk size of one sealed commit slot.
	commitSlotSize = BlockOverhead + commitPlainSize
)

// commitAAD is the additional data a commit slot is sealed with. Its length
// differs from frameAAD's, so a slot never opens as a frame or the reverse.
func commitAAD(slot int64) []byte {
	return append([]byte("encfs-commit"), byte(slot))
}

// sealCommit seals r for its slot.
func (c *blockCrypto) sealCommit(r commitRecord) []byte {
	var pt [commitPlainSize]byte
	binary.LittleEndian.PutUint64(pt[0:], uint64(r.seq))   // #nosec G115 -- non-negative
	binary.LittleEndian.PutUint64(pt[8:], uint64(r.count)) // #nosec G115 -- non-negative
	binary.LittleEndian.PutUint64(pt[16:], uint64(r.end))  // #nosec G115 -- non-negative
	// #nosec G407 -- the nonce argument MUST be nil: this AEAD is cipher.NewGCMWithRandomNonce (see sealFrame).
	return c.aead.Seal(nil, nil, pt[:], commitAAD(r.seq%commitSlots))
}

// openCommit authenticates the commit slot at index slot.
func (c *blockCrypto) openCommit(sealed []byte, slot int64) (commitRecord, bool) {
	pt, err := c.aead.Open(nil, nil, sealed, commitAAD(slot))
	if err != nil || len(pt) != commitPlainSize {
		return commitRecord{}, false
	}
	r := commitRecord{
		seq:   int64(binary.LittleEndian.Uint64(pt[0:])),  // #nosec G115 -- range checked below
		count: int64(binary.LittleEndian.Uint64(pt[8:])),  // #nosec G115 -- range checked below
		end:   int64(binary.LittleEndian.Uint64(pt[16:])), // #nosec G115 -- range checked below
	}
	if r.seq < 0 || r.count < 0 || r.end < 0 || r.seq%commitSlots != slot {
		return commitRecord{}, false
	}
	return r, true
}

// framedDataStart is the physical offset of the first frame of a framed
// file with header h: the header, then its commit slots.
func framedDataStart(h *header) int64 {
	return int64(h.totalSize()) + commitSlots*commitSlotSize
}

// freshCommitSlots returns both commit slots of a framed log with no
// frames whose data starts at start.
func (c *blockCrypto) freshCommitSlots(start int64) []byte {
	out := make([]byte, 0, commitSlots*commitSlotSize)
	for seq := int64(0); seq < commitSlots; seq++ {
		out = append(out, c.sealCommit(commitRecord{seq: seq, end: start})...)
	}
	return out
}

// readCommit returns the newest authentic commit slot, if any.
func (f *encryptedFile) readCommit(physicalSize int64) (commitRecord, bool, error) {
	var best commitRecord
	found := false
	base := f.headerSize - commitSlots*commitSlotSize
	for slot := int64(0); slot < commitSlots; slot++ {
		off := base + slot*commitSlotSize
		if off+commitSlotSize > physicalSize {
			continue
		}
		buf := make([]byte, commitSlotSize)
		if _, err := f.base.ReadAt(buf, off); err != nil {
			return commitRecord{}, false, err
		}
		if r, ok := f.crypto.openCommit(buf, slot); ok && (!found || r.seq > best.seq) {
			best, found = r, true
		}
	}
	return best, found, nil
}

// commitLocked records every frame on disk as durable in the older commit
// slot and syncs it. The frames themselves must already be synced.
func (f *encryptedFile) commitLocked() error {
	r := commitRecord{seq: f.commitSeq + 1, count: int64(len(f.frames)), end: f.physEnd}
	off := f.headerSize - commitSlots*commitSlotSize + (r.seq%commitSlots)*commitSlotSize
	if _, err := f.base.WriteAt(f.crypto.sealCommit(r), off); err != nil {
		return err
	}
	if err := f.base.Sync(); err != nil {
		return err
	}
	f.commitSeq, f.durable = r.seq, r.count
	return nil
}

// zeroFrom reports whether every byte of the base file in [from, to) is
// zero (true for an empty range).
// fixedLogLogicalSize is physicalToLogicalSize for a WAL or MANIFEST in the
// fixed-size block layout, written before the framed layout existed. That
// layout re-encrypts the trailing partial block and rewrites it in place on
// every Sync, so a power cut during the rewrite tears a block that already
// held synced bytes: the tear itself destroyed that block's previous
// content, and no reader can bring it back (which is why WALs and
// MANIFESTs are now framed). The layout records no durable boundary, so
// nothing tells a torn rewrite from corruption of synced data: a final
// block shorter than BlockOverhead may be a synced record whose ciphertext
// was later truncated, so it refuses the open with
// TrailingBlockTooShortError; a final block that fails authentication
// refuses the open with ErrAuthFailed, and an earlier block that fails
// authentication fails its read with ErrAuthFailed.
func (f *encryptedFile) fixedLogLogicalSize(payload int64) (int64, error) {
	if payload < 0 {
		return 0, &NegativePayloadLengthError{Length: payload}
	}
	if payload == 0 {
		return 0, nil
	}
	last := payload / int64(CiphertextBlockSize)
	lastLen := payload % int64(CiphertextBlockSize)
	if lastLen == 0 {
		last, lastLen = last-1, int64(CiphertextBlockSize)
	}
	if lastLen < int64(BlockOverhead) {
		// The old layout records no durable boundary, so a short final
		// block may be a synced record truncated later: refuse it rather
		// than drop acknowledged records.
		return 0, &TrailingBlockTooShortError{Bytes: lastLen}
	}
	buf := make([]byte, lastLen)
	if _, err := f.base.ReadAt(buf, f.physicalOffsetForBlock(last)); err != nil {
		return 0, err
	}
	pt, err := f.crypto.decrypt(buf)
	if err != nil {
		// The old layout records no durable boundary, so a full-length
		// final block that fails authentication may hold synced data:
		// refuse it rather than open the log short.
		return 0, fmt.Errorf("%w: final block %d of an old-format log", err, last)
	}
	return last*int64(BlockSize) + int64(len(pt)), nil
}

// physicalToLogicalSize maps the byte length of an encfs payload (the
// region after the header) to its plaintext length. Returns an error
// when the payload length is not a valid encfs framing.
func physicalToLogicalSize(payloadBytes int64) (int64, error) {
	if payloadBytes < 0 {
		return 0, &NegativePayloadLengthError{Length: payloadBytes}
	}
	if payloadBytes == 0 {
		return 0, nil
	}
	fullBlocks := payloadBytes / int64(CiphertextBlockSize)
	rem := payloadBytes % int64(CiphertextBlockSize)
	if rem == 0 {
		return fullBlocks * int64(BlockSize), nil
	}
	if rem < int64(BlockOverhead) {
		return 0, &TrailingBlockTooShortError{Bytes: rem}
	}
	return fullBlocks*int64(BlockSize) + (rem - int64(BlockOverhead)), nil
}

// physicalOffsetForBlock returns the byte offset within the underlying
// base file at which block `blockIndex` begins.
func (f *encryptedFile) physicalOffsetForBlock(blockIndex int64) int64 {
	return f.headerSize + blockIndex*int64(CiphertextBlockSize)
}

// Close releases the underlying file. If the file holds an
// in-memory tail with unflushed bytes the tail is encrypted and
// written before close. Close is idempotent.
func (f *encryptedFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if !f.readOnly {
		if err := f.flushTailLocked(); err != nil {
			_ = f.base.Close()
			return err
		}
	}
	return f.base.Close()
}

// Read implements io.Reader against the file's logical sequential
// read cursor.
func (f *encryptedFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	pos := f.seqPos
	f.mu.Unlock()
	n, err := f.ReadAt(p, pos)
	if n > 0 {
		f.mu.Lock()
		f.seqPos += int64(n)
		f.mu.Unlock()
	}
	return n, err
}

// ReadAt implements io.ReaderAt by decrypting the encfs blocks that
// cover the requested byte range and returning the requested slice.
func (f *encryptedFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, ErrNegativeOffset
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, ErrFileClosed
	}
	logicalSize := f.logicalSize
	flushedSize := f.flushedSize
	tail := append([]byte(nil), f.tail...)
	frames := f.frames
	f.mu.Unlock()

	if off >= logicalSize {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > logicalSize-off {
		want = int(logicalSize - off)
	}
	end := off + int64(want)

	written := 0
	for cursor := off; cursor < end; {
		var plaintext []byte
		var start int64
		if cursor >= flushedSize {
			// The in-memory tail covers logical range [flushedSize, logicalSize).
			start, plaintext = flushedSize, tail
		} else {
			var err error
			start, plaintext, err = f.readDurable(cursor, flushedSize, frames)
			if err != nil {
				return written, err
			}
		}
		intra := cursor - start
		copyLen := int64(len(plaintext)) - intra
		if copyLen > end-cursor {
			copyLen = end - cursor
		}
		if copyLen <= 0 {
			break
		}
		copy(p[written:written+int(copyLen)], plaintext[intra:intra+copyLen])
		written += int(copyLen)
		cursor += copyLen
	}
	if written < len(p) {
		return written, io.EOF
	}
	return written, nil
}

// readDurable decrypts the on-disk block holding logical offset cursor
// (cursor < flushedSize) and returns its logical start and plaintext.
// frames is the framed layout's frame index, as of the caller's snapshot.
func (f *encryptedFile) readDurable(cursor, flushedSize int64, frames []frameRef) (int64, []byte, error) {
	if f.framed {
		i := sort.Search(len(frames), func(i int) bool {
			return frames[i].logical+int64(frames[i].n) > cursor
		})
		if i == len(frames) {
			return 0, nil, io.ErrUnexpectedEOF
		}
		fr := frames[i]
		sealed := make([]byte, BlockOverhead+fr.n)
		if _, err := f.base.ReadAt(sealed, fr.phys+frameLenSize); err != nil {
			return 0, nil, err
		}
		pt, err := f.crypto.openFrame(sealed, int64(i), fr.n)
		return fr.logical, pt, err
	}
	blockIndex := cursor / int64(BlockSize)
	blockStart := blockIndex * int64(BlockSize)
	cipherLen := int64(CiphertextBlockSize)
	if (blockIndex+1)*int64(BlockSize) > flushedSize {
		// Trailing partial block already flushed once; its on-disk size
		// is the partial size + overhead.
		cipherLen = (flushedSize - blockStart) + int64(BlockOverhead)
	}
	cipherBuf := make([]byte, cipherLen)
	if _, err := f.base.ReadAt(cipherBuf, f.physicalOffsetForBlock(blockIndex)); err != nil {
		return 0, nil, err
	}
	pt, err := f.crypto.decrypt(cipherBuf)
	return blockStart, pt, err
}

// Write implements io.Writer by appending to the file. Plaintext is
// buffered in memory until full blocks accumulate; full blocks are
// encrypted and written to disk immediately. The trailing partial
// block remains in memory until Sync or Close.
func (f *encryptedFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrFileClosed
	}
	if f.readOnly {
		return 0, ErrReadOnly
	}
	return f.appendLocked(p)
}

// WriteAt is supported only at the file's current logical end; it
// then behaves as Write. Mid-file rewrites return ErrWriteAtUnsupported.
func (f *encryptedFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrFileClosed
	}
	if f.readOnly {
		return 0, ErrReadOnly
	}
	if off != f.logicalSize {
		return 0, ErrWriteAtUnsupported
	}
	return f.appendLocked(p)
}

// appendLocked appends p to the file. Caller must hold f.mu.
func (f *encryptedFile) appendLocked(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		room := BlockSize - len(f.tail)
		if room == 0 {
			if err := f.flushFullBlockLocked(); err != nil {
				return written, err
			}
			room = BlockSize - len(f.tail)
		}
		take := len(p)
		if take > room {
			take = room
		}
		f.tail = append(f.tail, p[:take]...)
		f.logicalSize += int64(take)
		written += take
		p = p[take:]
	}
	return written, nil
}

// flushFullBlockLocked writes f.tail (which is exactly BlockSize bytes
// long) as a full encrypted block to disk and resets f.tail. Caller
// must hold f.mu.
func (f *encryptedFile) flushFullBlockLocked() error {
	if len(f.tail) != BlockSize {
		return &FlushBlockSizeError{Expected: BlockSize, Got: len(f.tail)}
	}
	if f.framed {
		return f.writeFrameLocked()
	}
	ct, err := f.crypto.encrypt(f.tail)
	if err != nil {
		return err
	}
	blockIndex := f.flushedSize / int64(BlockSize)
	if _, err := f.base.WriteAt(ct, f.physicalOffsetForBlock(blockIndex)); err != nil {
		return err
	}
	f.flushedSize += int64(BlockSize)
	f.tail = f.tail[:0]
	return nil
}

// flushTailLocked writes the in-memory tail as a (possibly partial)
// encrypted block to disk. Called from Sync and Close. Caller must
// hold f.mu.
func (f *encryptedFile) flushTailLocked() error {
	if len(f.tail) == 0 {
		return nil
	}
	if f.framed {
		return f.writeFrameLocked()
	}
	ct, err := f.crypto.encrypt(f.tail)
	if err != nil {
		return err
	}
	blockIndex := f.flushedSize / int64(BlockSize)
	if _, err := f.base.WriteAt(ct, f.physicalOffsetForBlock(blockIndex)); err != nil {
		return err
	}
	// flushedSize is intentionally NOT advanced here: a subsequent
	// Write may extend the same trailing block, in which case we will
	// re-encrypt with a fresh inline nonce and overwrite the same disk
	// region. A power cut during that overwrite tears bytes an earlier
	// Sync made durable, which is why append-and-sync files (WALs and
	// MANIFESTs) use the framed layout instead (writeFrameLocked).
	return nil
}

// writeFrameLocked seals f.tail as the next frame of a framed file, writes
// it at the physical end and clears the tail. Caller must hold f.mu.
//
// The framed layout (header flag headerFlagFramed) is for append-and-sync
// files. Each frame is a little-endian uint32 plaintext length followed by
// the AEAD block (inline nonce, ciphertext, tag), sealed with the frame's
// index and length as additional data, so frames cannot be reordered,
// spliced or resized. A frame holds at most BlockSize bytes and is
// seal-on-sync: once written it is never rewritten, and the Write after a
// Sync starts a new frame. A power cut can therefore tear only bytes no
// Sync acknowledged. Logical offsets are found through the frame index
// built on open (scanFrames), not by fixed-size arithmetic.
//
// Two sealed commit slots sit between the header and the first frame. A
// Sync that wrote frames syncs them, then records the frame count and
// their physical end in the older slot and syncs again: that is the
// durable boundary. On open every frame inside the boundary must
// authenticate and end where the slot says, or the log is refused as
// corrupt; only bytes past it (never acknowledged) are dropped as torn.
func (f *encryptedFile) writeFrameLocked() error {
	if len(f.tail) == 0 {
		return nil
	}
	frame, err := f.crypto.sealFrame(f.tail, int64(len(f.frames)))
	if err != nil {
		return err
	}
	if _, err := f.base.WriteAt(frame, f.physEnd); err != nil {
		return err
	}
	f.frames = append(f.frames, frameRef{phys: f.physEnd, logical: f.flushedSize, n: len(f.tail)})
	f.physEnd += int64(len(frame))
	f.flushedSize += int64(len(f.tail))
	f.tail = f.tail[:0]
	return nil
}

// Sync persists buffered data to non-volatile storage by encrypting
// and writing the trailing partial block (if any) and then forwarding
// to the underlying file's Sync.
func (f *encryptedFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrFileClosed
	}
	if f.readOnly {
		return f.base.Sync()
	}
	if err := f.flushTailLocked(); err != nil {
		return err
	}
	if err := f.base.Sync(); err != nil {
		return err
	}
	if !f.framed || int64(len(f.frames)) == f.durable {
		return nil
	}
	// The frames are on disk: only now record them as durable, so a
	// frame torn by a power cut is never inside the commit boundary.
	return f.commitLocked()
}

// Stat returns a FileInfo whose Size reports the logical (plaintext)
// length of the file, hiding the encryption overhead from Pebble.
func (f *encryptedFile) Stat() (os.FileInfo, error) {
	fi, err := f.base.Stat()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	logicalSize := f.logicalSize
	f.mu.Unlock()
	return &encFileInfo{FileInfo: fi, logicalSize: logicalSize}, nil
}

// Preallocate is forwarded to the base file when it is a
// filesys.Preallocator, and is otherwise a no-op. The encryption overhead
// is small enough that Pebble's preallocation hint remains useful even
// without a translated offset.
func (f *encryptedFile) Preallocate(offset, length int64) error {
	if p, ok := f.base.(filesys.Preallocator); ok {
		return p.Preallocate(offset, length)
	}
	return nil
}

// Prefetch is forwarded to the base file, when it is a
// filesys.Prefetcher, with the encryption-overhead adjusted offset, and is
// otherwise a no-op. Prefetch is a hint; over-fetching is harmless.
func (f *encryptedFile) Prefetch(offset, length int64) error {
	if p, ok := f.base.(filesys.Prefetcher); ok {
		return p.Prefetch(offset+f.headerSize, length)
	}
	return nil
}

// encFileInfo wraps an underlying os.FileInfo and overrides Size to
// report the logical plaintext length. All other methods (Name,
// Mode, ModTime, IsDir, Sys) are promoted from the embedded
// base FileInfo via Go's method promotion rules.
type encFileInfo struct {
	os.FileInfo
	logicalSize int64
}

func (i *encFileInfo) Size() int64 { return i.logicalSize }

// ErrNegativeOffset is returned by ReadAt when given a negative offset.
var ErrNegativeOffset = errors.New("encfs: negative offset")

// NegativePayloadLengthError is returned when an encfs payload length is
// negative, indicating a corrupt or invalid framing.
type NegativePayloadLengthError struct {
	Length int64
}

func (e *NegativePayloadLengthError) Error() string {
	return fmt.Sprintf("encfs: payload length is negative (%d)", e.Length)
}

// TrailingBlockTooShortError is returned when the final encfs block is smaller
// than the per-block overhead, indicating truncation or corruption.
type TrailingBlockTooShortError struct {
	Bytes int64
}

func (e *TrailingBlockTooShortError) Error() string {
	return fmt.Sprintf("encfs: trailing block too short (%d bytes)", e.Bytes)
}

// FlushBlockSizeError is returned when flushFullBlockLocked is invoked with a
// tail buffer that is not exactly BlockSize bytes long.
type FlushBlockSizeError struct {
	Expected int
	Got      int
}

func (e *FlushBlockSizeError) Error() string {
	return fmt.Sprintf("encfs: flushFullBlock expected %d bytes, got %d", e.Expected, e.Got)
}
