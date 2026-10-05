// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package file provides a file-backed storage engine with a flat,
// content-addressed on-disk layout: each key's file is named by a
// cryptographic hash of the key, sharded into a small, fixed depth of
// directories. See Engine for why (go-qrdb#592).
package file

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// Compile-time interface checks.
var (
	_ storage.Storage = (*Engine)(nil)
	_ storage.Syncer  = (*Engine)(nil)
)

// Engine is a file-based storage engine with a hashed, flat on-disk layout.
//
// Each key's file is named by the hex SHA-256 digest of the key (stdlib
// crypto/sha256; no hand-rolled hashing), sharded two levels deep by the
// first four hex characters of that digest (two characters per level, 256
// subdirectories per level) so no single directory ever holds more than a
// bounded fraction of the keyspace's files. The path a key maps to therefore
// has CONSTANT length and depth whatever the key's own length or content:
// no two keys can collide on it (SHA-256 collisions are not a practical
// concern), no key's structure can produce an invalid or oversized path
// component, and no key is a filesystem-meaningful prefix of another the way
// '/' bytes used to make one.
//
// Each file stores the record [keyLen:4 BE][key][value]: the real key
// travels with its value, so Get and the iterator can verify the stored key
// equals the one that was asked for rather than trusting the hash alone, and
// List/the iterator can recover every key without a separate index.
//
// This is the second on-disk layout this engine has had (go-qrdb#592
// replaced one-file/one-directory per key with this one) and the two are
// incompatible: New refuses to open a non-empty data directory that was not
// written by this exact format (see formatMarkerFileName).
type Engine struct {
	dir    string
	closed atomic.Bool

	// journalMu serializes batch commits: the commit journal is a single
	// file in the engine root, so two concurrent commits must not
	// interleave publish/apply/clear.
	journalMu sync.Mutex
}

const (
	// formatMarkerFileName names the marker New checks before opening a
	// non-empty data directory, and writes when creating a fresh one. See
	// FormatMarkerMissingError and FormatVersionMismatchError.
	formatMarkerFileName = ".qrdb-file-format"

	// formatVersion is this build's on-disk format. Bump it, and the error
	// messages that name it, whenever the layout or record format changes
	// again; there is no migration between versions, by policy.
	formatVersion = "2"

	// lostAndFoundDirName is the directory mkfs/fsck creates at the root of
	// a freshly formatted ext2/3/4 filesystem (and recreates after a repair)
	// -- never written by any layout this engine has had, old or current,
	// and never meaningful to it. A root that holds nothing else is a FRESH
	// mount, not qrdb data left by an unmarked layout, so ensureFormatMarker
	// excludes it before deciding whether the directory already holds
	// something it must refuse to open in place (go-qrdb#592 F3).
	lostAndFoundDirName = "lost+found"

	// shardHexLen is the number of hex characters of a key's digest that
	// name each of the two shard directory levels (two levels of two
	// characters: 256 subdirectories per level, 65,536 leaf directories in
	// total, a fixed and bounded number regardless of keyspace size).
	shardHexLen = 2

	// keyHeaderLen is the width, in bytes, of the big-endian key-length
	// prefix at the start of every stored record.
	keyHeaderLen = 4

	// hashHexLen is the length of a hex-encoded SHA-256 digest, and so the
	// exact length of every record's filename. Used to recognise a record
	// file during a directory walk without reading it.
	hashHexLen = sha256.Size * 2
)

// New creates a new file-based storage engine rooted at dir. The directory
// is created if it does not exist, with every directory entry created along
// the way fsynced into its parent so the creation itself is crash-durable
// (mkdirAllDurable).
//
// A FRESH (non-existent or empty) directory is initialised with this
// build's format marker. A directory that already holds data is opened only
// if that marker is present and names this exact format version; see
// FormatMarkerMissingError and FormatVersionMismatchError.
//
// An interrupted batch commit from a previous process is completed here (see
// recoverJournal); a corrupt journal fails the open rather than silently
// serving a half-applied batch.
func New(dir string) (*Engine, error) {
	if dir == "" {
		return nil, storage.ErrInvalidPath
	}
	if err := mkdirAllDurable(dir); err != nil {
		return nil, err
	}
	e := &Engine{dir: dir}
	if err := e.ensureFormatMarker(); err != nil {
		return nil, err
	}
	if err := e.recoverJournal(); err != nil {
		return nil, err
	}
	if err := e.cleanupTempSetFiles(); err != nil {
		return nil, err
	}
	return e, nil
}

// cleanupTempSetFiles removes every ".tmp-set-*" file under the engine
// root: Set's own crash leftovers from a process that died between
// creating one (os.CreateTemp) and its final fsync.Rename into place
// (go-qrdb#592 Low). None ever named a committed record -- isHashFileName
// already excludes them from List, the iterator, and Get -- so leaving one
// in place is disk-space hygiene debt, not a correctness problem; this runs
// once, at Open, rather than having every later Set, List, or iterator scan
// check for one.
func (e *Engine) cleanupTempSetFiles() error {
	return filepath.Walk(e.dir, func(path string, info os.FileInfo, err error) error { // #nosec G122 -- internal storage directory, no symlink risk
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !strings.HasPrefix(info.Name(), ".tmp-set-") {
			return nil
		}
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		return nil
	})
}

// checkKey refuses an op this engine cannot store at all. Today that is only
// the empty key — no storage.Storage backend holds one — but the point of
// having ONE function is that every caller who must refuse the same things
// Set and Delete do calls this one: commitBufferedOps (batch.go) checks
// every buffered op against it BEFORE writing the commit journal, so a
// refused op leaves neither a journal nor a partial write (go-qrdb#592 B2/B3)
// — the same guarantee Set and Delete give when called directly.
func checkKey(key []byte) error {
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	return nil
}

// hashHex returns the hex-encoded SHA-256 digest of key.
func hashHex(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}

// shardDir returns the two-level shard directory a digest's record lives
// under.
func (e *Engine) shardDir(digest string) string {
	return filepath.Join(e.dir, digest[:shardHexLen], digest[shardHexLen:2*shardHexLen])
}

// recordPaths returns key's shard directory and the full path of its record
// file within it, computing the digest once for both.
func (e *Engine) recordPaths(key []byte) (dir, path string) {
	digest := hashHex(key)
	dir = e.shardDir(digest)
	return dir, filepath.Join(dir, digest)
}

// encodeRecord renders key and value as the on-disk record format:
// [keyLen:4 BE][key][value]. value is the remainder of the record and
// carries no length of its own — its size is exactly len(record) minus the
// header and the key.
func encodeRecord(key, value []byte) []byte {
	buf := make([]byte, keyHeaderLen+len(key)+len(value))
	binary.BigEndian.PutUint32(buf[0:keyHeaderLen], uint32(len(key))) // #nosec G115 -- bounded by protocol.MaxClientKeyLength (65535) before a key ever reaches storage
	copy(buf[keyHeaderLen:], key)
	copy(buf[keyHeaderLen+len(key):], value)
	return buf
}

// decodeRecord parses record's stored key and value. The returned slices
// alias record; callers that retain them past the buffer's lifetime must
// copy. path is used only to attribute a RecordCorruptError.
func decodeRecord(path string, record []byte) (key, value []byte, err error) {
	if len(record) < keyHeaderLen {
		return nil, nil, &RecordCorruptError{Path: path, Reason: "shorter than the key-length header"}
	}
	keyLen := binary.BigEndian.Uint32(record[0:keyHeaderLen])
	if uint64(keyHeaderLen)+uint64(keyLen) > uint64(len(record)) {
		return nil, nil, &RecordCorruptError{Path: path, Reason: "declared key length exceeds the record's size"}
	}
	key = record[keyHeaderLen : keyHeaderLen+keyLen]
	value = record[keyHeaderLen+keyLen:]
	return key, value, nil
}

// isHashFileName reports whether name has the exact shape of a record
// filename: a lowercase hex SHA-256 digest. Every other file this engine
// places under its root or a shard directory — the format marker, the
// commit journal, and either one's in-flight temp files — has a different
// shape, so this single check is what separates "a key's record" from
// "engine bookkeeping" during a directory walk, without needing to name
// every bookkeeping file here.
func isHashFileName(name string) bool {
	if len(name) != hashHexLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Get retrieves the value for the given key. Returns nil, nil when the key
// does not exist per the storage.Storage contract.
//
// The record's stored key is checked against key before its value is
// returned: with a cryptographic hash naming the file, a mismatch is not a
// realistic collision — it is the signature of corruption, and Get refuses
// rather than silently returning a value that provably belongs to a
// different key (KeyMismatchError).
func (e *Engine) Get(key []byte) ([]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return nil, err
	}
	_, path := e.recordPaths(key)
	record, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	storedKey, value, err := decodeRecord(path, record)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(storedKey, key) {
		return nil, &KeyMismatchError{Path: path}
	}
	out := make([]byte, len(value))
	copy(out, value)
	return out, nil
}

// Set durably stores a key-value pair: the record is written to a temp file
// in the key's shard directory (created if absent), fsynced, and atomically
// renamed into place, then that directory is fsynced. A crash therefore
// leaves either the old record or the new one, never a torn file — the
// durability contract the raft state-machine storage seam (raft.sm_storage:
// file) relies on.
func (e *Engine) Set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return err
	}
	dir, path := e.recordPaths(key)
	if err := mkdirAllDurable(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-set-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	record := encodeRecord(key, value)
	if _, err := tmp.Write(record); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// fsync.Rename is the rename and its directory sync as one operation. The
	// temp file and the target share dir, so it is synced exactly once -- the
	// same work the separate os.Rename + directory-fsync pair did, with no second
	// place for the pairing to be got wrong (go-qrdb#268).
	if err := fsync.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Delete removes a key from storage. The key's shard directories are left in
// place even when nothing else is left in them: they are a small, FIXED set
// (65,536 possible leaf directories at most, see shardHexLen) shared by
// every key that happens to hash into them, so pruning would race a
// concurrent Set of an unrelated key hashing into the same shard — and
// unlike the previous per-key directory layout, there is no unbounded growth
// to prune in the first place.
func (e *Engine) Delete(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return err
	}
	dir, path := e.recordPaths(key)
	if rmErr := os.Remove(path); rmErr != nil {
		if os.IsNotExist(rmErr) {
			return nil
		}
		return rmErr
	}
	// Make the removal durable: fsync the directory so a crash cannot
	// resurrect the deleted record.
	return fsync.Dir(dir)
}

// List returns all keys matching the given prefix, in ascending byte order.
//
// Because a key's filename carries no information about the key itself
// (it is a hash), every candidate record under the engine root must be
// opened and decoded to recover its key — unlike the previous, path-derived
// layout, where a keys-only scan never opened a file at all. That cost is
// the deliberate tradeoff of a flat, content-addressed layout: constant,
// bounded path shape for every key, in exchange for List and the iterator
// needing to read every record once per call.
func (e *Engine) List(prefix []byte) ([][]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	var keys [][]byte
	err := filepath.Walk(e.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// path == e.dir means the engine's OWN root failed to stat --
			// not a benign race with a concurrent Set or Delete but a real
			// fault (the directory this engine owns is gone), so it is
			// never skipped, ENOENT or not. See fileIterator.load: a
			// concurrent Set's rename-into-place, or a concurrent Delete,
			// CAN remove any OTHER entry between Walk reading its parent
			// directory and stat'ing it; that race is not a scan fault and
			// is skipped (go-qrdb#592 F2).
			if path != e.dir && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !isHashFileName(info.Name()) {
			return nil
		}
		record, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				return nil
			}
			return rerr
		}
		storedKey, _, derr := decodeRecord(path, record)
		if derr != nil {
			return derr
		}
		if bytes.HasPrefix(storedKey, prefix) {
			k := make([]byte, len(storedKey))
			copy(k, storedKey)
			keys = append(keys, k)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys, nil
}

// Exists reports whether the key exists in storage. Unlike Get, this does
// not open the record to verify the stored key: a presence check is exactly
// what callers of Exists want, and a mismatched record is vanishingly
// unlikely (see Get) and, if it ever happened, would still be caught the
// moment that key is actually read.
func (e *Engine) Exists(key []byte) (bool, error) {
	if e.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return false, err
	}
	_, path := e.recordPaths(key)
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Batch creates a new write batch for atomic multi-key operations. A closed
// engine returns a batch that reports the closure from every operation.
func (e *Engine) Batch() storage.Batch {
	if e.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(e)
}

// NewIterator creates an iterator for scanning key ranges. A closed engine
// returns an iterator that reports the closure through Error.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if e.closed.Load() {
		return storage.NewClosedIterator()
	}
	return newIterator(e, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
func (e *Engine) Snapshot() (storage.Snapshot, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	return newSnapshot(e)
}

// Sync fsyncs the engine root directory. Individual Set/Delete/Batch
// operations are already durable on their own; Sync exists to satisfy the
// storage.Syncer seam for callers that checkpoint explicitly.
func (e *Engine) Sync() error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	return fsync.Dir(e.dir)
}

// Close marks the engine as closed. Subsequent operations return ClosedError.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}

// mkdirAllDurable creates every directory component of dir that does not
// already exist, like os.MkdirAll, and fsyncs each newly created
// directory's PARENT immediately after creating it — publishing that new
// directory ENTRY, which a crash can otherwise lose even though the files
// written inside it later are made durable on their own (Set, the commit
// journal). It stops at the first ancestor that already exists: nothing
// changed there, so there is nothing to make durable.
func mkdirAllDurable(dir string) error {
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return &os.PathError{Op: "mkdir", Path: dir, Err: os.ErrExist}
		}
		// dir already exists: a concurrent Set targeting a sibling key in
		// the same shard directory may have just created it and not yet
		// reached its own fsync.Dir below (go-qrdb#592 Low). Fsync the
		// parent here too, so every caller that reaches this point --
		// racing the creator or simply arriving later -- returns only once
		// the directory ENTRY is durable, never racing ahead of it.
		return fsync.Dir(filepath.Dir(dir))
	} else if !os.IsNotExist(err) {
		return err
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := mkdirAllDurable(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if os.IsExist(err) {
			// Lost the creation race to a concurrent Set. Its own
			// fsync.Dir below may not have landed yet, and this call needs
			// the identical guarantee before its caller writes into dir,
			// so sync independently rather than trust the winner's
			// in-flight call.
			return fsync.Dir(filepath.Dir(dir))
		}
		return err
	}
	return fsync.Dir(filepath.Dir(dir))
}

// ensureFormatMarker refuses to open a non-empty data directory that was
// not written by this exact on-disk format (see FormatMarkerMissingError,
// FormatVersionMismatchError), and writes the marker into a fresh one.
func (e *Engine) ensureFormatMarker() error {
	// A crash between creating the marker's temp file and renaming it leaves
	// a stray temp file with no committed marker; clean it up first so it
	// cannot be mistaken for "this directory already holds data".
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasPrefix(ent.Name(), ".tmp-format-") {
			_ = os.Remove(filepath.Join(e.dir, ent.Name()))
		}
	}

	markerPath := filepath.Join(e.dir, formatMarkerFileName)
	data, err := os.ReadFile(markerPath)
	switch {
	case err == nil:
		if string(data) != formatVersion {
			return &FormatVersionMismatchError{Found: string(data), Want: formatVersion}
		}
		return nil
	case os.IsNotExist(err):
		entries, err = os.ReadDir(e.dir)
		if err != nil {
			return err
		}
		for _, ent := range entries {
			if ent.IsDir() && ent.Name() == lostAndFoundDirName {
				continue
			}
			return &FormatMarkerMissingError{Dir: e.dir}
		}
		return e.writeFormatMarker()
	default:
		return err
	}
}

// writeFormatMarker durably publishes formatMarkerFileName naming
// formatVersion, via the same temp-file-plus-rename pattern Set uses for a
// record.
func (e *Engine) writeFormatMarker() error {
	tmp, err := os.CreateTemp(e.dir, ".tmp-format-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(formatVersion); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := fsync.Rename(tmpName, filepath.Join(e.dir, formatMarkerFileName)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
