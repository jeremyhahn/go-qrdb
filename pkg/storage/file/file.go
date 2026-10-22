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
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/util/filelock"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// Compile-time interface checks.
var (
	_ storage.Storage = (*Engine)(nil)
	_ storage.Syncer  = (*Engine)(nil)

	_ storage.Exclusive  = (*Engine)(nil)
	_ storage.ReadWriter = lockedView{}
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
//
// The CONTENTS of each record and of the commit journal are read and written
// through fs: the OS file system, or the seal barrier's encrypting file system
// when the engine is opened WithEncryptor, the same one the pebble backend
// writes through. Under it every record, key and value alike, is ciphertext
// at rest. The format marker alone is plaintext (see writeFormatMarker).
// Name operations (listing, stat, rename, remove, directory fsync) go to the
// OS directly: the encrypting file system passes names through unchanged, so
// the hashed file names are the same either way.
//
// # Sharing a directory between processes
//
// More than one process may open the same directory (an application that loads
// a PKCS#11 module and the daemon that serves it, say). Every mutation -- Set,
// Delete, a batch commit, open-time recovery -- holds an exclusive advisory OS
// lock (flock(2) / LockFileEx, on lockFileName in the root) for its duration,
// and Exclusive lets a caller hold the same lock across a read-check-write, so
// a guarded write (compare-and-swap, save-if-absent) is atomic across
// processes. Readers take no lock and stay correct: a record is replaced by
// renaming a fully written temporary file over it, so a Get sees the old record
// or the new one, never a torn one.
//
// The lock is advisory and is honoured only by processes using this engine, and
// it excludes only writers that reach the files through the same lock file; the
// directory must be on a local filesystem (flock is not reliable over NFS).
type Engine struct {
	dir    string
	fs     vfs.FS
	atRest string
	closed atomic.Bool

	// writeMu is the in-process half of the engine's write exclusion, and
	// lock the cross-process half; lockWrites takes both, in that order. Every
	// mutation holds them: a Set, a Delete, a batch commit (whose journal is a
	// single file in the engine root, so two commits must not interleave
	// publish/apply/clear) and the open-time recovery. An OS file lock belongs
	// to the open file, so two goroutines sharing lock would both hold it --
	// writeMu is what stops that. A nil lock (an Engine built by hand in a
	// test) is only ever used through the unlocked internals.
	writeMu sync.Mutex
	lock    *filelock.File

	// durableShardDirs remembers, by path, every shard directory this
	// Engine has already confirmed durable (see ensureShardDirDurable): a
	// concurrent-safe, in-process cache so Set's hot path pays the parent
	// fsync mkdirAllDurable always does only the first time any key hashes
	// into a given shard directory, not on every subsequent Set into it
	// (go-qrdb#592 performance). It is memory only and never persisted: a
	// fresh Engine over the same directory starts with an empty cache and
	// simply re-confirms (and re-fsyncs) each shard directory the first
	// time THIS instance touches it again, which is correct and cheap --
	// the directory is already durable from before; the fsync it repeats
	// is redundant work, not a correctness requirement.
	durableShardDirs sync.Map

	// nameKey is an encrypted store's record-name key once recordNameKey
	// has read it through the barrier, and nil until then; a plaintext
	// store never sets it. nameKeyMu serialises its creation, so goroutines
	// applying one batch in parallel cannot each mint a different key.
	nameKey   atomic.Pointer[[]byte]
	nameKeyMu sync.Mutex

	// syncs times every fsync the engine issues on its write path (see
	// timedSync). Nil, in an Engine built by hand, times nothing.
	syncs *syncwatch.Observer
}

// timedSync runs one fsync through e's observer (observeSync).
func (e *Engine) timedSync(fsyncFn func() error) error {
	return observeSync(e.syncs, fsyncFn)
}

// observeSync runs one fsync, recording its duration through syncs and
// warning when it was slow. A failed fsync is timed too: a disk that fails
// slowly stalls the write as much as one that succeeds slowly. A nil syncs
// times nothing.
func observeSync(syncs *syncwatch.Observer, fsyncFn func() error) error {
	start := syncs.Now()
	err := fsyncFn()
	syncs.Since(start)
	return err
}

const (
	// formatMarkerFileName names the marker New checks before opening a
	// non-empty data directory, and writes when creating a fresh one. See
	// FormatMarkerMissingError and FormatVersionMismatchError.
	formatMarkerFileName = ".qrdb-file-format"

	// lockFileName names the file in the engine root that carries the
	// cross-process write lock (see Engine). It holds no data and is never
	// removed. ensureFormatMarker does not count it as content, because New
	// creates it before deciding whether the directory is fresh.
	lockFileName = ".qrdb-file-lock"

	// formatVersion is this build's on-disk format. Bump it, and the error
	// messages that name it, whenever the layout or record format changes
	// again; there is no migration between versions, by policy. The marker
	// carries the at-rest posture after the version; every record's and the
	// journal's contents go through the engine's file system, and an
	// encrypted store names its records under its record-name key
	// (nameKeyFileName).
	formatVersion = "4"

	// nameKeyFileName names the file in an encrypted store's root that holds
	// its record-name key: nameKeyLen random bytes, written through the
	// engine's file system and so readable only through the barrier. A
	// record file in an encrypted store is named by the HMAC-SHA-256 of its
	// key under this key, so a name reveals nothing about the key to anyone
	// without the barrier; a plaintext store names records by the bare
	// SHA-256 and has no such file. The key is written before the store's
	// first record, so a store without it holds no records.
	nameKeyFileName = ".qrdb-file-names"

	// nameKeyLen is the record-name key's length in bytes: SHA-256's output
	// length L, the least RFC 2104 §3 recommends for an HMAC key.
	nameKeyLen = sha256.Size

	// atRestPlaintext and atRestEncrypted are the at-rest postures the
	// format marker records after the version ("4/encrypted"), so a store
	// is never opened under the other one: a plaintext store under the
	// barrier would serve its plaintext records beside new ciphertext ones,
	// and an encrypted store without it would decode ciphertext as records.
	atRestPlaintext = "plaintext"
	atRestEncrypted = "encrypted"

	// Temp-file prefixes for the four crash-safe publishes (writeDurable):
	// a record, the commit journal, the format marker and the record-name
	// key. None has the shape of a record name (isHashFileName), and
	// leftovers are removed at open.
	tmpSetPrefix     = ".tmp-set-"
	tmpJournalPrefix = ".tmp-journal-"
	tmpFormatPrefix  = ".tmp-format-"
	tmpNamesPrefix   = ".tmp-names-"

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

	// hashHexLen is the length of a hex-encoded SHA-256 digest or
	// HMAC-SHA-256 tag, and so the exact length of every record's filename. Used to recognise a record
	// file during a directory walk without reading it.
	hashHexLen = sha256.Size * 2
)

// Encryptor layers at-rest encryption over the file system the engine reads
// and writes file contents through. *atrest.Encryptor, the seal barrier's
// encrypting file system that the pebble backend also writes through,
// satisfies it. One whose Enabled reports false leaves the engine plaintext.
type Encryptor interface {
	Enabled() bool
	Wrap(base vfs.FS) vfs.FS
}

// Option configures New.
type Option func(*options)

// options is what the Options set.
type options struct {
	encryptor Encryptor
	syncs     *syncwatch.Observer
}

// WithEncryptor encrypts every file's contents at rest under enc. A nil or
// disabled enc leaves the engine plaintext.
func WithEncryptor(enc Encryptor) Option {
	return func(o *options) { o.encryptor = enc }
}

// WithSyncObserver times the engine's fsyncs through o instead of the
// default observer, which records into the process registry under
// backend="file" and names the data directory in its slow-sync warning. A
// nil o keeps the default.
func WithSyncObserver(o *syncwatch.Observer) Option {
	return func(opts *options) {
		if o != nil {
			opts.syncs = o
		}
	}
}

// New creates a new file-based storage engine rooted at dir. The directory
// is created if it does not exist, with every directory entry created along
// the way fsynced into its parent so the creation itself is crash-durable
// (mkdirAllDurable); those fsyncs are timed like every other.
//
// A FRESH (non-existent or empty) directory is initialised with this
// build's format marker. A directory that already holds data is opened only
// if that marker is present and names this exact format version; see
// FormatMarkerMissingError and FormatVersionMismatchError.
//
// An interrupted batch commit from a previous process is completed here (see
// recoverJournal); a corrupt journal fails the open rather than silently
// serving a half-applied batch.
//
// With WithEncryptor the store is encrypted at rest; a directory written in
// the other posture is refused (AtRestMismatchError).
//
// The open-time work (format marker, journal recovery, stray temp-file cleanup)
// runs under the same cross-process write lock as every write, so a second
// process opening a directory the first is writing to waits for the write in
// flight instead of removing its temporary file or replaying its journal. The
// lock is released on every return path and the lock file is closed when the
// open fails.
func New(dir string, opts ...Option) (*Engine, error) {
	if dir == "" {
		return nil, storage.ErrInvalidPath
	}
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.syncs == nil {
		cfg.syncs = syncwatch.ForStorage(syncwatch.BackendFile, syncwatch.WithAttrs(slog.String("dir", dir)))
	}
	if err := mkdirAllDurable(dir, cfg.syncs); err != nil {
		return nil, err
	}
	lock, err := filelock.Open(filepath.Join(dir, lockFileName))
	if err != nil {
		return nil, err
	}
	e := &Engine{dir: dir, fs: vfs.Default, atRest: atRestPlaintext, lock: lock, syncs: cfg.syncs}
	if cfg.encryptor != nil && cfg.encryptor.Enabled() {
		e.fs = cfg.encryptor.Wrap(vfs.Default)
		e.atRest = atRestEncrypted
	}
	if err := e.recoverUnderLock(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return e, nil
}

// recoverUnderLock performs the open-time work of New while holding the write
// lock.
func (e *Engine) recoverUnderLock() (err error) {
	if err := e.acquireWrites(); err != nil {
		return err
	}
	defer func() { err = e.unlockWrites(err) }()

	// The format is validated before the journal is replayed: a directory
	// this build refuses must not be written to on the way to refusing it.
	if err := e.ensureFormatMarker(); err != nil {
		return err
	}
	if err := e.recoverJournal(); err != nil {
		return err
	}
	return e.cleanupTempSetFiles()
}

// lockWrites takes the engine's write exclusion (see acquireWrites) and then
// finishes any commit a dead process left journaled, so no mutation is
// admitted over a half-applied batch. On any error nothing is held.
func (e *Engine) lockWrites() error {
	if err := e.acquireWrites(); err != nil {
		return err
	}
	// A process that died mid-commit left its journal behind; finish that
	// commit before admitting any other mutation.
	if err := e.replayJournal(); err != nil {
		_ = e.lock.Unlock()
		e.writeMu.Unlock()
		return err
	}
	return nil
}

// acquireWrites takes writeMu, then the cross-process lock, without touching
// the directory's contents. It refuses a closed Engine, and re-checks after
// taking writeMu because Close takes writeMu before closing the lock file, so
// a lock file observed open here stays open until unlockWrites. On any error
// nothing is held.
func (e *Engine) acquireWrites() error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	e.writeMu.Lock()
	if e.closed.Load() {
		e.writeMu.Unlock()
		return &storage.ClosedError{}
	}
	if err := e.lock.Lock(); err != nil {
		e.writeMu.Unlock()
		return err
	}
	return nil
}

// unlockWrites releases what lockWrites took and returns err, or the release
// failure when err is nil, so a write that succeeded but could not give the lock
// back is reported rather than hiding a stuck lock. writeMu is released even
// then; filelock.Unlock closes the file on failure so the OS lets go regardless.
func (e *Engine) unlockWrites(err error) error {
	uerr := e.lock.Unlock()
	e.writeMu.Unlock()
	if err != nil {
		return err
	}
	return uerr
}

// Exclusive runs fn while holding the engine's exclusive write lock, in this
// process and across every process sharing the directory. See storage.Exclusive.
func (e *Engine) Exclusive(fn func(storage.ReadWriter) error) (err error) {
	if err := e.lockWrites(); err != nil {
		return err
	}
	defer func() { err = e.unlockWrites(err) }()
	return fn(lockedView{e})
}

// lockedView is the storage.ReadWriter Exclusive hands to its callback: reads
// and writes that do not take the write lock, because the caller already holds
// it.
type lockedView struct {
	e *Engine
}

// Get reads key.
func (v lockedView) Get(key []byte) ([]byte, error) { return v.e.Get(key) }

// Set writes key without taking the write lock.
func (v lockedView) Set(key, value []byte) error { return v.e.set(key, value) }

// Delete removes key without taking the write lock.
func (v lockedView) Delete(key []byte) error { return v.e.del(key) }

// walkShardDirs calls fn once for every path filepath.Walk would report
// beneath one of this engine's own shard directories -- never for the
// engine root, and never for a root-level entry this engine did not create.
//
// filepath.Walk reads a directory's contents BEFORE it ever calls fn for
// that directory: fn learns about a failed os.ReadDir only as the err
// argument of the one call it gets for that path, with the read already
// attempted, so returning filepath.SkipDir from fn cannot prevent a read
// that already happened. Calling filepath.Walk on the engine ROOT would
// therefore attempt to read a foreign root-level entry's contents -- most
// notably lost+found, root-owned and mode 0700 by convention on a real
// mount -- before this engine's own callback runs at all, which is too late
// to skip it. Listing the root with os.ReadDir instead, and walking only the
// entries that already look like one of this engine's own shard directories
// (isShardDirName), never makes that attempt in the first place
// (go-qrdb#592 F3).
func (e *Engine) walkShardDirs(fn filepath.WalkFunc) error {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() || !isShardDirName(ent.Name()) {
			continue
		}
		if err := filepath.Walk(filepath.Join(e.dir, ent.Name()), fn); err != nil { // #nosec G122 -- internal storage directory, no symlink risk
			return err
		}
	}
	return nil
}

// cleanupTempSetFiles removes every ".tmp-set-*" file under one of this
// engine's shard directories: Set's own crash leftovers from a process that
// died between creating one (os.CreateTemp) and its final fsync.Rename into
// place (go-qrdb#592 Low). None ever named a committed record --
// isHashFileName already excludes them from List, the iterator, and Get --
// so leaving one in place is disk-space hygiene debt, not a correctness
// problem; this runs once, at Open, rather than having every later Set,
// List, or iterator scan check for one.
func (e *Engine) cleanupTempSetFiles() error {
	return e.walkShardDirs(func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !strings.HasPrefix(info.Name(), tmpSetPrefix) {
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
// file within it, computing the record name once for both. create says
// whether an encrypted store without a record-name key mints one, as a write
// does; for a read the missing key file's not-exist error is returned, which
// means the store holds no records (see nameKeyFileName).
func (e *Engine) recordPaths(key []byte, create bool) (dir, path string, err error) {
	name, err := e.recordName(key, create)
	if err != nil {
		return "", "", err
	}
	dir = e.shardDir(name)
	return dir, filepath.Join(dir, name), nil
}

// recordName returns the name of key's record file: the hex SHA-256 of key in
// a plaintext store, and the hex HMAC-SHA-256 (RFC 2104) of key under the
// store's record-name key in an encrypted one.
func (e *Engine) recordName(key []byte, create bool) (string, error) {
	if e.atRest != atRestEncrypted {
		return hashHex(key), nil
	}
	nameKey, err := e.recordNameKey(create)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, nameKey)
	mac.Write(key)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// recordNameKey returns an encrypted store's record-name key, reading it
// through the barrier the first time, so it fails with the barrier's error
// while the barrier is sealed. When the key file does not exist it returns
// that not-exist error, unless create is set: then it mints the key and
// writes it durably before returning it. A caller passing create holds the
// write lock, which makes the creation exclusive across processes;
// nameKeyMu makes it exclusive within this one.
func (e *Engine) recordNameKey(create bool) ([]byte, error) {
	if k := e.nameKey.Load(); k != nil {
		return *k, nil
	}
	e.nameKeyMu.Lock()
	defer e.nameKeyMu.Unlock()
	if k := e.nameKey.Load(); k != nil {
		return *k, nil
	}
	path := filepath.Join(e.dir, nameKeyFileName)
	k, err := readFile(e.fs, path)
	switch {
	case err == nil:
		if len(k) != nameKeyLen {
			return nil, &NameKeyLengthError{Path: path, Len: len(k), Want: nameKeyLen}
		}
	case create && isNotExist(err):
		k = make([]byte, nameKeyLen)
		if _, err := io.ReadFull(rand.Reader, k); err != nil {
			return nil, err
		}
		if err := e.writeDurable(e.fs, path, tmpNamesPrefix, k); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	e.nameKey.Store(&k)
	return k, nil
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
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isShardDirName reports whether name has the shape of one of this engine's
// own two shard-directory levels: exactly shardHexLen (2) lowercase hex
// characters. Every directory walk that starts at the engine root uses this
// to recognise, and descend into, only directories this engine itself could
// have created — never a foreign entry sitting at the root for a reason that
// has nothing to do with this engine, most notably lost+found, the
// directory mkfs/fsck creates at an ext2/3/4 mount root (root-owned, mode
// 0700 by convention) and recreates after a repair. A walk that tried to
// read lost+found's contents would fail with a permission error that is not
// this engine's fault and has nothing to do with its data; one that never
// descends into it in the first place cannot fail on it at all (go-qrdb#592
// F3).
func isShardDirName(name string) bool {
	if len(name) != shardHexLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
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
	_, path, err := e.recordPaths(key, false)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	record, err := readFile(e.fs, path)
	if err != nil {
		if isNotExist(err) {
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
// renamed into place, then that directory is fsynced (writeDurable). A crash therefore
// leaves either the old record or the new one, never a torn file — the
// durability contract the raft state-machine storage seam (raft.sm_storage:
// file) relies on. It holds the write lock (see Engine) for the duration.
func (e *Engine) Set(key, value []byte) (err error) {
	if err := e.lockWrites(); err != nil {
		return err
	}
	defer func() { err = e.unlockWrites(err) }()
	return e.set(key, value)
}

// set is Set without the write lock, for callers that already hold it: the
// open-time recovery, a batch commit and Exclusive.
func (e *Engine) set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return err
	}
	return e.writeRecord(key, value)
}

// writeRecord durably replaces the record for key. The caller holds the write
// lock.
func (e *Engine) writeRecord(key, value []byte) error {
	dir, path, err := e.recordPaths(key, true)
	if err != nil {
		return err
	}
	if err := e.ensureShardDirDurable(dir); err != nil {
		return err
	}
	return e.writeDurable(e.fs, path, tmpSetPrefix, encodeRecord(key, value))
}

// CompareAndSwap implements storage.GuardedStorage. The read, compare and write
// run under the engine's write lock, in this process and across every process
// sharing the directory (see Engine), so concurrent swaps on a key serialise
// wherever they run. The write goes through the same path as Set.
func (e *Engine) CompareAndSwap(ctx context.Context, key, expected, next []byte) (swapped bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := checkKey(key); err != nil {
		return false, err
	}
	if err := e.lockWrites(); err != nil {
		return false, err
	}
	defer func() { err = e.unlockWrites(err) }()

	current, err := e.Get(key)
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := e.set(key, next); err != nil {
		return false, err
	}
	return true, nil
}

// removeRecord removes the record file at path through an *os.Root opened on
// the store directory, so a path derived from a caller's key cannot name
// anything outside the store.
func (e *Engine) removeRecord(path string) error {
	rel, err := filepath.Rel(e.dir, path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.Remove(rel)
}

// mkdirShardDurable is mkdirAllDurable for a directory beneath the store root,
// which exists since New: it walks the components below the root through an
// *os.Root, creating each missing one and fsyncing its parent, so a path
// derived from a caller's key cannot reach outside the store.
func (e *Engine) mkdirShardDurable(dir string) error {
	rel, err := filepath.Rel(e.dir, dir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	cur := "."
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		next := filepath.Join(cur, part)
		info, err := root.Stat(next)
		switch {
		case err == nil:
			if !info.IsDir() {
				return &os.PathError{Op: "mkdir", Path: filepath.Join(e.dir, next), Err: os.ErrExist}
			}
		case os.IsNotExist(err):
			// A concurrent creator may win the race; either way the entry must
			// be durable before the caller writes into it.
			if err := root.Mkdir(next, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
		default:
			return err
		}
		if err := e.timedSync(func() error { return fsync.Dir(filepath.Join(e.dir, cur)) }); err != nil {
			return err
		}
		cur = next
	}
	return nil
}

// discardTemp removes the temp file name, best effort, on a failed write. The
// temp path derives from the caller's key, so it is removed through an
// *os.Root opened on the store directory: the removal cannot leave the store
// whatever the path holds.
func (e *Engine) discardTemp(name string) {
	rel, err := filepath.Rel(e.dir, name)
	if err != nil {
		return
	}
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	_ = root.Remove(rel)
}

// Delete removes a key from storage. The key's shard directories are left in
// place even when nothing else is left in them: they are a small, FIXED set
// (65,536 possible leaf directories at most, see shardHexLen) shared by
// every key that happens to hash into them, so pruning would race a
// concurrent Set of an unrelated key hashing into the same shard — and
// unlike the previous per-key directory layout, there is no unbounded growth
// to prune in the first place. It holds the write lock (see Engine) for the
// duration.
func (e *Engine) Delete(key []byte) (err error) {
	if err := e.lockWrites(); err != nil {
		return err
	}
	defer func() { err = e.unlockWrites(err) }()
	return e.del(key)
}

// del is Delete without the write lock, for callers that already hold it.
func (e *Engine) del(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return err
	}
	dir, path, err := e.recordPaths(key, false)
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	if rmErr := e.removeRecord(path); rmErr != nil {
		if os.IsNotExist(rmErr) {
			return nil
		}
		return rmErr
	}
	// Make the removal durable: fsync the directory so a crash cannot
	// resurrect the deleted record.
	return e.timedSync(func() error { return fsync.Dir(dir) })
}

// DeleteRange removes every key in [start, end); an empty end is unbounded.
// Hashed filenames carry no key order, so the range is found by filtering the
// full key listing, and each delete is individually durable (see Delete).
// It holds the write lock (see Engine) across the whole range.
func (e *Engine) DeleteRange(start, end []byte) (err error) {
	if err := storage.ValidateRange(start, end); err != nil {
		return err
	}
	if err := e.lockWrites(); err != nil {
		return err
	}
	defer func() { err = e.unlockWrites(err) }()
	return e.deleteRange(start, end)
}

// deleteRange is DeleteRange without the write lock, for callers that already
// hold it: a batch commit and open-time recovery.
func (e *Engine) deleteRange(start, end []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if err := storage.ValidateRange(start, end); err != nil {
		return err
	}
	keys, err := e.List(nil)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if bytes.Compare(key, start) < 0 {
			continue
		}
		if len(end) > 0 && bytes.Compare(key, end) >= 0 {
			continue
		}
		if err := e.del(key); err != nil {
			return err
		}
	}
	return nil
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
	err := e.walkShardDirs(func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// A concurrent Set's rename-into-place, or a concurrent
			// Delete, CAN remove an entry between walkShardDirs reading its
			// parent directory and stat'ing it; that race is not a scan
			// fault and is skipped (go-qrdb#592 F2). os.ReadDir(e.dir)
			// failing -- the engine's own root being gone, a real fault --
			// is surfaced directly by walkShardDirs itself and never
			// reaches this callback at all.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !isHashFileName(info.Name()) {
			return nil
		}
		record, rerr := readFile(e.fs, path)
		if rerr != nil {
			if isNotExist(rerr) {
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
// moment that key is actually read. In an encrypted store the record name
// needs the record-name key, so a store that has not read it yet answers
// with the barrier's error while the barrier is sealed.
func (e *Engine) Exists(key []byte) (bool, error) {
	if e.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if err := checkKey(key); err != nil {
		return false, err
	}
	_, path, err := e.recordPaths(key, false)
	if err == nil {
		_, err = os.Stat(path)
	}
	if err != nil {
		if isNotExist(err) {
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

// Snapshot creates a point-in-time snapshot for consistent reads. Like every
// read it takes no lock (see Engine).
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
	return e.timedSync(func() error { return fsync.Dir(e.dir) })
}

// Close marks the engine as closed and closes its lock file, releasing the
// cross-process lock. Subsequent operations return ClosedError. It waits for a
// write in flight, and is idempotent.
func (e *Engine) Close() error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.closed.Swap(true) {
		return nil
	}
	if e.lock == nil {
		return nil
	}
	return e.lock.Close()
}

// ensureShardDirDurable is mkdirAllDurable for one of e's own shard
// directories, memoized: a shard directory this Engine instance has already
// confirmed durable once is remembered in durableShardDirs, so Set's hot
// path -- every key that hashes into an already-touched shard directory --
// skips mkdirAllDurable, and so its parent fsync, entirely. The first Set
// into any given shard directory still pays the real cost; only repeat
// access to the SAME directory is free (go-qrdb#592 performance).
//
// A concurrent first touch of the same directory can race this check (one
// goroutine's Store has not landed before another's Load runs): both simply
// take the slow path once each, which is correct -- just not the full
// benefit of the cache on that one race -- and the entry either one stores
// makes every later Set to the same directory free regardless of which one
// won.
func (e *Engine) ensureShardDirDurable(dir string) error {
	if _, ok := e.durableShardDirs.Load(dir); ok {
		return nil
	}
	if err := e.mkdirShardDurable(dir); err != nil {
		return err
	}
	e.durableShardDirs.Store(dir, struct{}{})
	return nil
}

// mkdirAllDurable creates every directory component of dir that does not
// already exist, like os.MkdirAll, and fsyncs each newly created
// directory's PARENT immediately after creating it — publishing that new
// directory ENTRY, which a crash can otherwise lose even though the files
// written inside it later are made durable on their own (Set, the commit
// journal). It stops at the first ancestor that already exists: nothing
// changed there, so there is nothing to make durable. Every fsync is timed
// through syncs (observeSync), like the engine's other syncs.
func mkdirAllDurable(dir string, syncs *syncwatch.Observer) error {
	syncParent := func() error {
		return observeSync(syncs, func() error { return fsync.Dir(filepath.Dir(dir)) })
	}
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
		return syncParent()
	} else if !os.IsNotExist(err) {
		return err
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := mkdirAllDurable(parent, syncs); err != nil {
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
			return syncParent()
		}
		return err
	}
	return syncParent()
}

// isNotExist reports whether err says a file is absent. The engine's file
// system wraps OS errors, which os.IsNotExist does not unwrap.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// readFile returns the contents of the file at path, read through fs: the
// engine's file system decrypts when the engine is encrypted. Under a sealed
// barrier an encrypted file cannot be opened and the barrier's error is
// returned unwrapped (seal.ErrSealed).
func readFile(fs vfs.FS, path string) ([]byte, error) {
	f, err := fs.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// writeDurable publishes data at path crash-safely: it is written through fs
// to a temp file named tmpPrefix plus a random suffix in path's directory,
// made owner-only before any of data is written, fsynced, and renamed into
// place. fsync.Rename is the rename and its directory sync as one operation
// (go-qrdb#268). A crash leaves either the old file or the new one, never a
// torn one; a leftover temp file is removed at open, and a failed write's
// temp file through discardTemp. path lies beneath the store root.
func (e *Engine) writeDurable(fs vfs.FS, path, tmpPrefix string, data []byte) error {
	tmpName := filepath.Join(filepath.Dir(path), tmpPrefix+rand.Text())
	f, err := fs.Create(tmpName, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		e.discardTemp(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := e.timedSync(f.Sync); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		e.discardTemp(tmpName)
		return err
	}
	if err := e.timedSync(func() error { return fsync.Rename(tmpName, path) }); err != nil {
		e.discardTemp(tmpName)
		return err
	}
	return nil
}

// ensureFormatMarker refuses to open a non-empty data directory that was
// not written by this exact on-disk format (see FormatMarkerMissingError,
// FormatVersionMismatchError), and writes the marker into a fresh one.
func (e *Engine) ensureFormatMarker() error {
	// A crash between creating the marker's or the record-name key's temp
	// file and renaming it leaves a stray temp file with no committed file;
	// clean it up first so it cannot be mistaken for "this directory already
	// holds data".
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() && (strings.HasPrefix(ent.Name(), tmpFormatPrefix) || strings.HasPrefix(ent.Name(), tmpNamesPrefix)) {
			_ = os.Remove(filepath.Join(e.dir, ent.Name()))
		}
	}

	markerPath := filepath.Join(e.dir, formatMarkerFileName)
	data, err := readFile(vfs.Default, markerPath)
	switch {
	case err == nil:
		version, atRest, _ := strings.Cut(string(data), "/")
		if version != formatVersion {
			return &FormatVersionMismatchError{Found: version, Want: formatVersion}
		}
		if atRest != e.atRest {
			return &AtRestMismatchError{Dir: e.dir, Found: atRest, Want: e.atRest}
		}
		return nil
	case isNotExist(err):
		entries, err = os.ReadDir(e.dir)
		if err != nil {
			return err
		}
		for _, ent := range entries {
			if ent.IsDir() && ent.Name() == lostAndFoundDirName {
				continue
			}
			if !ent.IsDir() && ent.Name() == lockFileName {
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
// formatVersion and the engine's at-rest posture ("4/encrypted"). The marker
// is the one file always written in plaintext: it says how every other file
// must be read, so it has to be readable before that choice is made, and it
// holds no data.
func (e *Engine) writeFormatMarker() error {
	return e.writeDurable(vfs.Default, filepath.Join(e.dir, formatMarkerFileName), tmpFormatPrefix, []byte(formatMarker(e.atRest)))
}

// formatMarker is the content of formatMarkerFileName for a store with the
// given at-rest posture.
func formatMarker(atRest string) string {
	return formatVersion + "/" + atRest
}
