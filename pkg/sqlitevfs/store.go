// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlitevfs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// Backend is the versioned key-value store the page store reads and commits
// through.
type Backend interface {
	// Every method takes the caller's context first: ctx bounds that one
	// call, and a done ctx fails the call before it reads or writes.

	// Revision returns the newest committed revision; err is a failed
	// read.
	Revision(ctx context.Context) (int64, error)
	// GetAt returns key's value at revision rev. A key with no live value
	// at rev is found=false with a nil error; err is a failed read.
	GetAt(ctx context.Context, key []byte, rev int64) (value []byte, found bool, err error)
	// Commit applies every put and every delete at one new revision, or
	// none of them, and returns that revision. Every read check is
	// evaluated atomically with the write: one that fails is
	// ErrSnapshotChanged and writes nothing.
	Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (rev int64, err error)
	// Publish, at one new revision, puts dst + (key without src) for
	// every key live under prefix src with that key's newest committed
	// value, deletes those source keys, and applies puts and deletes; or
	// does none of it. A live count under src other than expectedKeys is
	// ErrPublishConflict and writes nothing; empty or overlapping src and
	// dst, or expectedKeys < 1, is ErrInvalidPublish. The read checks are
	// Commit's, evaluated atomically with the move: a failed one is
	// ErrSnapshotChanged and writes nothing.
	Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (rev int64, err error)
	// Keys returns every key with a live value at revision rev that
	// starts with prefix, in key order.
	Keys(ctx context.Context, prefix []byte, rev int64) ([][]byte, error)
}

// ReadCheck is one entry of a commit's read set: the commit applies only if
// Key's live state is unchanged since revision Rev. Found is whether Key had
// a live value at Rev: when true, Key must still have a live value whose mod
// revision is at most Rev; when false, Key must still have no live value (an
// absent key stays absent).
type ReadCheck struct {
	Key   []byte
	Rev   int64
	Found bool
}

// Store is the page store of one SQLite database file over a Backend.
type Store struct {
	backend Backend
	db      string
	metaKey []byte

	// stageThreshold is the dirty-page byte total above which a commit
	// goes through staging and one publish (Q2 A); stageChunkBytes bounds
	// the page bytes of each staging Backend.Commit.
	stageThreshold  int
	stageChunkBytes int
	// requestBytes is the transport request cap: a commit whose encoded
	// request (encodedRequestBytes) would reach it goes through staging,
	// and a staging commit is flushed before its request would reach it.
	requestBytes int

	// writer, when set, serializes this database's writers (Q3 A) through
	// the lock named lockKey; nil is the no-lock (optimistic/local) Store.
	writer  WriterLock
	lockKey string

	// optimistic is the opt-in optimistic writer mode (Q3 A): no writer
	// lock, and every commit carries the transaction's read set, so a
	// commit that read a page another writer changed since the snapshot is
	// ErrSnapshotChanged (the binding's SQLITE_BUSY_SNAPSHOT).
	optimistic bool

	// mu guards seenGen, seenFound and seen: the meta generation of this
	// Store's last snapshot pin, whether that pin found a meta record, and
	// whether it has pinned one.
	mu        sync.Mutex
	seenGen   uint64
	seenFound bool
	seen      bool
}

const (
	// DefaultStageThreshold is the default dirty-page byte total above
	// which a commit stages its pages before one publish.
	DefaultStageThreshold = 4 << 20
	// transportRequestBytes is the smallest default request limit of the
	// transports a remote page store commits through.
	transportRequestBytes = min(grpc.DefaultMaxMessageBytes, rest.DefaultMaxBodyBytes, quic.DefaultMaxPayloadBytes)

	// DefaultStageChunkBytes is the default page bytes per staging commit:
	// half the smallest default transport request limit, so the pages
	// base64 encoded by the JSON codec plus their keys and framing fit one
	// request, and well under go-quicraft's 8 MiB proto.MaxEntrySize.
	DefaultStageChunkBytes = transportRequestBytes / 2

	// requestEntryFraming bounds what one key of a commit request costs
	// beyond its base64-encoded key and value on any transport: field
	// names, quotes, separators, the revision of a read check, and
	// protobuf tags and lengths. requestEnvelopeFraming bounds the rest of
	// the request around its entries.
	requestEntryFraming    = 256
	requestEnvelopeFraming = 4096
)

// encodedEntryBytes is the most one entry with a key of keyLen bytes and a
// value of valueLen bytes adds to a commit request: both base64 encoded (the
// JSON codec's encoding of bytes, larger than the binary ones) plus
// requestEntryFraming.
func encodedEntryBytes(keyLen, valueLen int) int {
	return base64.StdEncoding.EncodedLen(keyLen) + base64.StdEncoding.EncodedLen(valueLen) + requestEntryFraming
}

// encodedRequestBytes bounds the encoded size of one Backend.Commit or
// Backend.Publish request carrying reads, puts and deletes; extraKeys are
// the bytes of any other keys it names (a publish's prefixes).
func encodedRequestBytes(reads []ReadCheck, puts map[string][]byte, deletes [][]byte, extraKeys ...[]byte) int {
	n := requestEnvelopeFraming
	for _, r := range reads {
		n += encodedEntryBytes(len(r.Key), 0)
	}
	for key, value := range puts {
		n += encodedEntryBytes(len(key), len(value))
	}
	for _, key := range deletes {
		n += encodedEntryBytes(len(key), 0)
	}
	for _, key := range extraKeys {
		n += encodedEntryBytes(len(key), 0)
	}
	return n
}

// splitDeletes returns the head of deletes that fits one request in which
// spent encoded bytes are already taken, and the rest.
func (s *Store) splitDeletes(spent int, deletes [][]byte) (head, rest [][]byte) {
	for i, key := range deletes {
		entry := encodedEntryBytes(len(key), 0)
		if spent+entry >= s.requestBytes {
			return deletes[:i], deletes[i:]
		}
		spent += entry
	}
	return deletes, nil
}

// trimAfter deletes rest, the keys a commit at rev could not carry under
// the transport cap, in chunks under it. Each chunk commits only while the
// meta record is the one rev wrote, so a later writer's pages are never
// deleted. Every key in rest reads as zeros under that record, and a later
// commit that grows over one deletes it, so a chunk that is refused or
// fails leaves only unreachable keys and is not the commit's error.
func (s *Store) trimAfter(ctx context.Context, rev int64, rest [][]byte) {
	guard := []ReadCheck{{Key: s.metaKey, Rev: rev, Found: true}}
	spent := encodedRequestBytes(guard, nil, nil)
	for len(rest) > 0 {
		head, tail := s.splitDeletes(spent, rest)
		if len(head) == 0 {
			return
		}
		if _, err := s.backend.Commit(ctx, guard, nil, head); err != nil {
			return
		}
		rest = tail
	}
}

// Option configures a Store built by NewStore.
type Option func(*Store) error

// WithStageThreshold sets the dirty-page byte total above which a commit
// goes through staging and one publish. A non-positive value is refused
// with ErrInvalidOption.
func WithStageThreshold(bytes int) Option {
	return func(s *Store) error {
		if bytes <= 0 {
			return fmt.Errorf("%w: stage threshold %d", ErrInvalidOption, bytes)
		}
		s.stageThreshold = bytes
		return nil
	}
}

// WithStageChunkBytes sets the most page bytes one staging commit writes. A
// non-positive value is refused with ErrInvalidOption.
func WithStageChunkBytes(bytes int) Option {
	return func(s *Store) error {
		if bytes <= 0 {
			return fmt.Errorf("%w: stage chunk size %d", ErrInvalidOption, bytes)
		}
		s.stageChunkBytes = bytes
		return nil
	}
}

// WithWriterLock serializes the Store's writers through w (Q3 A): a
// transaction writes only after Txn.Reserve took the lock, and holds it until
// Commit or Rollback. A nil lock is refused with ErrInvalidOption. Without
// this option or WithOptimistic (the default mode), writers take no lock and
// a commit whose database moved since its snapshot is ErrSnapshotChanged.
func WithWriterLock(w WriterLock) Option {
	return func(s *Store) error {
		if w == nil {
			return fmt.Errorf("%w: nil writer lock", ErrInvalidOption)
		}
		s.writer = w
		return nil
	}
}

// WithOptimistic selects optimistic writer mode (Q3 A, opt-in per
// connection): the Store takes no writer lock, a transaction records every
// page it reads from its snapshot, and Commit sends that read set with the
// write, so a commit that read a page another writer has since changed (or
// created) is ErrSnapshotChanged and writes nothing, while writers whose
// reads and writes are disjoint both commit. NewStore refuses it together
// with WithWriterLock (ErrOptimisticWithWriterLock).
func WithOptimistic() Option {
	return func(s *Store) error {
		s.optimistic = true
		return nil
	}
}

// NewStore returns the page store of database db over b. A nil backend is
// refused with ErrNilBackend and an invalid name with ErrInvalidDatabaseName.
func NewStore(b Backend, db string, opts ...Option) (*Store, error) {
	if b == nil {
		return nil, ErrNilBackend
	}
	metaKey, err := MetaKey(db)
	if err != nil {
		return nil, err
	}
	lockKey, err := WriterLockKey(db)
	if err != nil {
		return nil, err
	}
	s := &Store{
		backend:         b,
		db:              db,
		metaKey:         metaKey,
		lockKey:         lockKey,
		stageThreshold:  DefaultStageThreshold,
		stageChunkBytes: DefaultStageChunkBytes,
		requestBytes:    transportRequestBytes,
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	if s.optimistic && s.writer != nil {
		return nil, ErrOptimisticWithWriterLock
	}
	return s, nil
}

// DB returns the database name.
func (s *Store) DB() string {
	return s.db
}

// Optimistic reports whether the Store is in optimistic writer mode.
func (s *Store) Optimistic() bool {
	return s.optimistic
}

// readMeta reads the meta record at revision rev. An absent meta record is
// an empty database: found=false and a Meta with only FormatVersion set.
func (s *Store) readMeta(ctx context.Context, rev int64) (Meta, bool, error) {
	meta := Meta{FormatVersion: FormatVersion}
	value, found, err := s.backend.GetAt(ctx, s.metaKey, rev)
	if err != nil {
		return Meta{}, false, fmt.Errorf("sqlitevfs: read meta of %q at revision %d: %w", s.db, rev, err)
	}
	if found {
		if err := meta.UnmarshalBinary(value); err != nil {
			return Meta{}, false, err
		}
	}
	return meta, found, nil
}

// ImageKey is one key a restore of the database may write, and the bytes
// it writes there.
type ImageKey struct {
	Key   string
	Bytes int64
}

// ImageAt lists every key a restore to rev may write: the metadata record
// (at its size at rev, at least as large as the one the restore writes,
// which differs only in fixed-width counters) and every page of the
// database image at rev, each a full page. A restore can rewrite all of
// them even when the revision diff names only the metadata key, so this is
// the plan an admission or quota check prices. A database absent at rev
// has nothing to write but the cleared metadata.
func (s *Store) ImageAt(ctx context.Context, rev int64) ([]ImageKey, error) {
	value, found, err := s.backend.GetAt(ctx, s.metaKey, rev)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: read meta of %q at revision %d: %w", s.db, rev, err)
	}
	meta := Meta{FormatVersion: FormatVersion}
	if found {
		if err := meta.UnmarshalBinary(value); err != nil {
			return nil, err
		}
	}
	metaBytes := int64(len(value))
	if enc, err := meta.MarshalBinary(); err == nil && int64(len(enc)) > metaBytes {
		metaBytes = int64(len(enc))
	}
	out := make([]ImageKey, 0, int(meta.PageCount)+1)
	out = append(out, ImageKey{Key: string(s.metaKey), Bytes: metaBytes})
	for pgno := uint32(1); pgno <= meta.PageCount && pgno != 0; pgno++ {
		key, err := PageKey(s.db, pgno)
		if err != nil {
			return nil, err
		}
		out = append(out, ImageKey{Key: string(key), Bytes: int64(meta.PageSize)})
	}
	return out, nil
}

// Begin starts a transaction: it pins the backend's current revision and
// reads the meta record at it. An absent meta record is an empty database
// (size 0, page size not yet set). ctx is the context of every read the
// transaction makes (Begin's own, and every page read through ReadAt);
// Reserve, Commit and Rollback use the ctx they are given.
//
// If this Store pinned a different meta generation before, the database
// was restored underneath it (Q4 A): Begin records the new generation and
// returns ErrSnapshotChanged, and the next Begin succeeds. This is where a
// page cache will be dropped once there is one.
func (s *Store) Begin(ctx context.Context) (*Txn, error) {
	rev, err := s.backend.Revision(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: begin %q: %w", s.db, err)
	}
	return s.BeginAt(ctx, rev)
}

// BeginAt is Begin with the snapshot pinned at revision rev instead of the
// backend's current one: a caller that just committed at rev reads its own
// commit's snapshot, whatever committed since. rev must not be ahead of the
// backend's revision (the backend refuses a read there); the generation
// check is Begin's.
func (s *Store) BeginAt(ctx context.Context, rev int64) (*Txn, error) {
	if rev < 0 {
		return nil, fmt.Errorf("sqlitevfs: begin %q at negative revision %d", s.db, rev)
	}
	meta, found, err := s.readMeta(ctx, rev)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	// A database created since a pin that saw none is no restore.
	changed := s.seen && s.seenGen != meta.Generation && (s.seenFound || !found)
	s.seenGen = meta.Generation
	s.seenFound = found
	s.seen = true
	s.mu.Unlock()
	if changed {
		return nil, fmt.Errorf("%w: %q is at generation %d", ErrSnapshotChanged, s.db, meta.Generation)
	}
	return &Txn{
		store: s,
		ctx:   ctx,
		rev:   rev,
		base:  meta,
		meta:  meta,
		floor: meta.PageCount,
		dirty: make(map[uint32][]byte),

		baseFound: found,
	}, nil
}

// BumpGeneration is the restore hook of Q4 A: it reads the meta record at
// the current revision and commits it with Generation+1 in one
// Backend.Commit, returning that revision. The commit carries the meta
// record it read as a read check, so a commit that lands between the read
// and the write (ErrSnapshotChanged) is never overwritten: the bump reads
// again, at most restoreBumpAttempts times. A database with no meta record
// writes nothing and returns the current revision. ctx bounds the reads and
// the commits.
func (s *Store) BumpGeneration(ctx context.Context) (int64, error) {
	for range restoreBumpAttempts {
		rev, err := s.backend.Revision(ctx)
		if err != nil {
			return 0, fmt.Errorf("sqlitevfs: bump generation of %q: %w", s.db, err)
		}
		meta, found, err := s.readMeta(ctx, rev)
		if err != nil {
			return 0, err
		}
		if !found {
			return rev, nil
		}
		meta.Generation++
		value, err := meta.MarshalBinary()
		if err != nil {
			return 0, err
		}
		reads := []ReadCheck{{Key: s.metaKey, Rev: rev, Found: true}}
		newRev, err := s.backend.Commit(ctx, reads, map[string][]byte{string(s.metaKey): value}, nil)
		if errors.Is(err, ErrSnapshotChanged) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("sqlitevfs: bump generation of %q: %w", s.db, err)
		}
		return newRev, nil
	}
	return 0, fmt.Errorf("%w: %q moved on each of %d generation bump attempts", ErrSnapshotChanged, s.db, restoreBumpAttempts)
}

// restoreBumpAttempts bounds how often RestoreToRevision begins its
// transaction again when a commit moved the database between the snapshot
// it read and its restore commit.
const restoreBumpAttempts = 8

// RestoreToRevision is the restore of Q4 A as one page-store commit: it
// restores the database file to its image at revision rev and bumps its
// generation in the same revision, so no transaction ever reads a
// half-restored file or a restored file under an unbumped generation.
//
// It begins a transaction at the current revision and, on a Store with a
// writer lock, reserves it (a lock another writer holds is ErrBusy). It reads
// the meta record at rev and its pages, truncates the transaction to the
// size at rev and writes only the pages whose bytes differ from the current
// ones, deletes every stage key live now (no stage key is part of an image;
// with a writer lock, Reserve swept them), then commits once through the
// normal commit path (above the stage
// threshold: staging and one publish) with Generation = max(generation now,
// generation at rev) + 1 and the change counter advanced. Generations only
// grow along the live history, so the result is past every generation any
// connection has seen, and every transaction begun before is
// ErrSnapshotChanged at its next Begin or Commit.
//
// A database with no meta record at rev and one now is removed: its meta
// record, every page and every stage key are deleted in one Backend.Commit.
// One with none now but one at rev is created. When the image at rev is the
// current one, nothing is written and the current revision is returned. A
// commit that moved the database between the snapshot and the restore
// commit (ErrSnapshotChanged) begins again, at most restoreBumpAttempts
// times. It returns the revision of the restore commit; ctx bounds every
// read, lock call and commit.
func (s *Store) RestoreToRevision(ctx context.Context, rev int64) (int64, error) {
	if rev < 0 {
		return 0, fmt.Errorf("sqlitevfs: restore %q to negative revision %d", s.db, rev)
	}
	for range restoreBumpAttempts {
		newRev, err := s.restoreOnce(ctx, rev, -1)
		if errors.Is(err, ErrSnapshotChanged) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("sqlitevfs: restore %q to revision %d: %w", s.db, rev, err)
		}
		return newRev, nil
	}
	return 0, fmt.Errorf("%w: %q moved on each of %d restore attempts", ErrSnapshotChanged, s.db, restoreBumpAttempts)
}

// RestoreToRevisionFrom is RestoreToRevision for a caller that observed the
// database at revision observed and must not overwrite anything committed
// since: its one attempt begins its transaction at observed, so a commit
// after observed is ErrSnapshotChanged (on a Store with a writer lock, at
// Reserve) and nothing is written. It never retries.
func (s *Store) RestoreToRevisionFrom(ctx context.Context, rev, observed int64) (int64, error) {
	if rev < 0 {
		return 0, fmt.Errorf("sqlitevfs: restore %q to negative revision %d", s.db, rev)
	}
	if observed < 0 {
		return 0, fmt.Errorf("sqlitevfs: restore %q observed at negative revision %d", s.db, observed)
	}
	newRev, err := s.restoreOnce(ctx, rev, observed)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: restore %q to revision %d from revision %d: %w", s.db, rev, observed, err)
	}
	return newRev, nil
}

// restoreOnce is one attempt of RestoreToRevision. A non-negative observed
// pins the transaction's snapshot there instead of at the current revision.
func (s *Store) restoreOnce(ctx context.Context, rev, observed int64) (int64, error) {
	target, targetFound, err := s.readMeta(ctx, rev)
	if err != nil {
		return 0, err
	}
	var t *Txn
	if observed >= 0 {
		t, err = s.BeginAt(ctx, observed)
	} else {
		t, err = s.Begin(ctx)
	}
	if err != nil {
		return 0, err
	}
	if err := t.Reserve(ctx); err != nil {
		_ = t.Rollback(ctx)
		return 0, err
	}
	if !targetFound {
		if !t.baseFound {
			_ = t.Rollback(ctx)
			if observed >= 0 {
				return s.unchangedSince(ctx, t.rev)
			}
			return t.rev, nil
		}
		t.remove = true
		t.wrote = true
		return t.Commit(ctx)
	}
	if err := t.restoreImage(rev, target); err != nil {
		_ = t.Rollback(ctx)
		return 0, err
	}
	// Reserve already swept the stages of a Store with a writer lock.
	root, err := StageRoot(s.db)
	if err == nil && s.writer == nil {
		t.stageDeletes, err = s.backend.Keys(ctx, root, t.rev)
	}
	if err != nil {
		_ = t.Rollback(ctx)
		return 0, err
	}
	if len(t.stageDeletes) > 0 {
		t.wrote = true
	}
	t.restoreGen = max(t.base.Generation, target.Generation) + 1
	return t.Commit(ctx)
}

// unchangedSince returns observed when the database still has no meta record
// at the current revision, and ErrSnapshotChanged when one was created since.
func (s *Store) unchangedSince(ctx context.Context, observed int64) (int64, error) {
	now, err := s.backend.Revision(ctx)
	if err != nil {
		return 0, err
	}
	if _, found, err := s.readMeta(ctx, now); err != nil {
		return 0, err
	} else if found {
		return 0, fmt.Errorf("%w: %q was created after revision %d", ErrSnapshotChanged, s.db, observed)
	}
	return observed, nil
}

// restoreImage buffers the writes that turn the transaction's file into the
// image target describes at revision rev: a different page size starts the
// file over, the size is set to the size at rev, and every page whose bytes
// differ from the transaction's copy is written (an absent page at rev reads
// as zeros).
func (t *Txn) restoreImage(rev int64, target Meta) error {
	if target.PageSize == 0 {
		return t.Truncate(0)
	}
	if t.meta.PageSize != 0 && t.meta.PageSize != target.PageSize {
		if err := t.Truncate(0); err != nil {
			return err
		}
		t.meta.PageSize = 0
	}
	if t.meta.PageSize == 0 {
		t.meta.PageSize = target.PageSize
		t.wrote = true
	}
	if err := t.Truncate(int64(target.PageCount) * int64(target.PageSize)); err != nil {
		return err
	}
	for pgno := uint32(1); pgno <= target.PageCount && pgno != 0; pgno++ {
		key, err := PageKey(t.store.db, pgno)
		if err != nil {
			return err
		}
		want, found, err := t.store.backend.GetAt(t.ctx, key, rev)
		if err != nil {
			return fmt.Errorf("read page %d at revision %d: %w", pgno, rev, err)
		}
		if !found {
			want = make([]byte, target.PageSize)
		}
		if len(want) != int(target.PageSize) {
			return fmt.Errorf("page %d at revision %d is %d bytes, want %d", pgno, rev, len(want), target.PageSize)
		}
		have, err := t.page(pgno)
		if err != nil {
			return err
		}
		if bytes.Equal(have, want) {
			continue
		}
		if _, err := t.WriteAt(want, int64(pgno-1)*int64(target.PageSize)); err != nil {
			return err
		}
	}
	return nil
}

// commitRemoval deletes the database in one Backend.Commit: its meta record
// and every page and stage key live at revision now, carrying reads.
func (t *Txn) commitRemoval(ctx context.Context, now int64, reads []ReadCheck) (int64, error) {
	s := t.store
	seen := map[string]bool{string(s.metaKey): true}
	deletes := [][]byte{s.metaKey}
	for _, root := range []func(string) ([]byte, error){PagePrefix, StageRoot} {
		prefix, err := root(s.db)
		if err != nil {
			return 0, err
		}
		keys, err := s.backend.Keys(ctx, prefix, now)
		if err != nil {
			return 0, fmt.Errorf("sqlitevfs: remove %q: %w", s.db, err)
		}
		for _, key := range keys {
			if !seen[string(key)] {
				seen[string(key)] = true
				deletes = append(deletes, key)
			}
		}
	}
	rev, err := s.backend.Commit(ctx, reads, nil, deletes)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: remove %q: %w", s.db, err)
	}
	return rev, nil
}

// sweepStages deletes, in one Backend.Commit, every staged page key of the
// database live at revision rev. The caller holds the writer lock.
func (s *Store) sweepStages(ctx context.Context, rev int64) error {
	root, err := StageRoot(s.db)
	if err != nil {
		return err
	}
	keys, err := s.backend.Keys(ctx, root, rev)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	_, err = s.backend.Commit(ctx, nil, nil, keys)
	return err
}

// Txn is one transaction on a database file: a snapshot at the revision
// Begin pinned plus a buffer of the transaction's own writes. It offers the
// file semantics a VFS binding calls (size, read, write, truncate) and
// commits the buffer as one Backend.Commit, or, above the stage threshold,
// as staging commits and one Backend.Publish. A Txn is not safe for concurrent
// use.
type Txn struct {
	store *Store
	// ctx is the read context: the context Begin was given, used by every
	// page and meta read the transaction makes.
	// ReadAt is io-shaped (SQLite's xRead) and carries no context of its own.
	ctx context.Context
	rev int64
	// base is the meta record at rev; meta is base with the buffered
	// writes and truncations applied.
	base Meta
	meta Meta
	// floor is the lowest page count this transaction has truncated to:
	// a page above it that is not in dirty reads as zeros and is deleted
	// at commit, since the snapshot's copy is no longer part of the file.
	floor uint32
	dirty map[uint32][]byte
	wrote bool
	// rewrote is set once a page already in dirty is written again or the
	// file shrinks (a cache spill, then the final write, or a journal
	// played back over it and the file truncated to its old size); only
	// then does Commit compare dirty pages with the snapshot.
	rewrote bool
	done    bool
	// reserved is set once Reserve succeeded; token is the writer lock's
	// fencing token while this transaction holds it (held is true).
	reserved bool
	held     bool
	token    int64
	// reads is the read set of an optimistic transaction: every page key
	// read from the snapshot at rev, and whether it had a live value.
	reads map[string]bool
	// baseFound is whether the meta record existed at rev.
	baseFound bool
	// restoreGen, when non-zero, is the generation a RestoreToRevision
	// commit writes instead of base's; remove makes that commit delete
	// the database instead (its image at the restored revision is none).
	restoreGen uint64
	remove     bool
	// stageDeletes are the stage keys live at rev that a RestoreToRevision
	// commit deletes with its write (no stage key is part of an image).
	stageDeletes [][]byte
}

// ChangeCounter is the meta change counter of the transaction's
// snapshot: the count of write transactions committed before it.
func (t *Txn) ChangeCounter() uint64 {
	return t.base.ChangeCounter
}

// Revision returns the revision the transaction reads at.
func (t *Txn) Revision() int64 {
	return t.rev
}

// Size returns the file size in bytes, including buffered writes:
// PageCount*PageSize.
func (t *Txn) Size() int64 {
	return int64(t.meta.PageCount) * int64(t.meta.PageSize)
}

// Reserve makes the transaction the database's writer: SQLite's
// xLock(RESERVED). A second call is a no-op. On a Store without a writer lock
// it only marks the transaction reserved (optimistic/local mode). With one it
// takes the lock without blocking: a lock held by another writer is ErrBusy
// at once (the binding maps it to SQLITE_BUSY; SQLite's busy handler
// retries). It then re-reads the meta record at the current revision: if the
// generation or the change counter moved since this transaction's snapshot,
// the snapshot is stale and cannot become the writer, so Reserve releases
// the lock and returns ErrSnapshotChanged (SQLITE_BUSY_SNAPSHOT, as SQLite on
// a file whose change counter moved).
//
// Holding the lock, Reserve sweeps abandoned stages: only the lock holder
// stages pages, so every key under StageRoot seen under the lock was left by
// a writer that crashed or lost its lease, and Reserve deletes them all in
// one Backend.Commit. A failed sweep releases the lock and is returned.
// Every lock call, read and sweep Reserve makes uses its own ctx.
func (t *Txn) Reserve(ctx context.Context) error {
	if t.done {
		return ErrTxnDone
	}
	if t.reserved {
		return nil
	}
	s := t.store
	if s.writer == nil {
		t.reserved = true
		return nil
	}
	token, err := s.writer.Acquire(ctx, s.lockKey)
	if err != nil {
		return err
	}
	t.token = token
	t.held = true
	rev, err := s.backend.Revision(ctx)
	if err != nil {
		_ = t.release(ctx)
		return fmt.Errorf("sqlitevfs: reserve %q: %w", s.db, err)
	}
	current, _, err := s.readMeta(ctx, rev)
	if err != nil {
		_ = t.release(ctx)
		return err
	}
	if current.Generation != t.base.Generation || current.ChangeCounter != t.base.ChangeCounter {
		_ = t.release(ctx)
		return fmt.Errorf("%w: %q moved from generation %d change counter %d to generation %d change counter %d",
			ErrSnapshotChanged, s.db, t.base.Generation, t.base.ChangeCounter, current.Generation, current.ChangeCounter)
	}
	if err := s.sweepStages(ctx, rev); err != nil {
		_ = t.release(ctx)
		return fmt.Errorf("sqlitevfs: sweep abandoned stages of %q: %w", s.db, err)
	}
	t.reserved = true
	return nil
}

// release gives up the writer lock if this transaction holds it.
func (t *Txn) release(ctx context.Context) error {
	if !t.held {
		return nil
	}
	t.held = false
	return t.store.writer.Release(ctx, t.store.lockKey, t.token)
}

// ReadAt reads len(p) bytes at offset off from the buffer, else from the
// pages at the pinned revision. A read wholly past the end zero-fills p and
// returns 0 and io.EOF; a read that runs past the end zero-fills the rest of
// p and returns the bytes read and io.ErrUnexpectedEOF (SQLite's
// SQLITE_IOERR_SHORT_READ contract). ReadAt is io-shaped (SQLite's xRead):
// it takes no context, and its page reads use the context Begin was given.
func (t *Txn) ReadAt(p []byte, off int64) (int, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	if off < 0 {
		return 0, fmt.Errorf("sqlitevfs: negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	size := t.Size()
	if off >= size {
		clear(p)
		return 0, io.EOF
	}
	want := int64(len(p))
	if remain := size - off; want > remain {
		want = remain
	}
	pageSize := int64(t.meta.PageSize)
	n := 0
	for int64(n) < want {
		pos := off + int64(n)
		pgno := uint32(pos/pageSize) + 1 //nolint:gosec // G115: pos < size, so the page number fits PageCount's uint32
		page, err := t.page(pgno)
		if err != nil {
			return n, err
		}
		n += copy(p[n:want], page[pos%pageSize:])
	}
	if n < len(p) {
		clear(p[n:])
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

// page returns page pgno as the transaction sees it: the buffered copy, else
// zeros above the truncation floor, else the page at the pinned revision
// (zeros when absent, a hole).
func (t *Txn) page(pgno uint32) ([]byte, error) {
	if page, ok := t.dirty[pgno]; ok {
		return page, nil
	}
	if pgno > t.floor {
		return make([]byte, t.meta.PageSize), nil
	}
	key, err := PageKey(t.store.db, pgno)
	if err != nil {
		return nil, err
	}
	value, found, err := t.store.backend.GetAt(t.ctx, key, t.rev)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: read page %d of %q at revision %d: %w", pgno, t.store.db, t.rev, err)
	}
	if t.store.optimistic {
		if t.reads == nil {
			t.reads = make(map[string]bool)
		}
		t.reads[string(key)] = found
	}
	if !found {
		return make([]byte, t.meta.PageSize), nil
	}
	if len(value) != int(t.meta.PageSize) {
		return nil, fmt.Errorf("sqlitevfs: page %d of %q is %d bytes, want %d", pgno, t.store.db, len(value), t.meta.PageSize)
	}
	return value, nil
}

// WriteAt buffers a write of p at offset off. The first write to an empty
// database sets the page size to len(p), which must be a page size SQLite
// allows (ErrInvalidMeta) at an offset that is a multiple of it. Every later
// write must be exactly one page at a page-aligned offset: a write of
// another valid page size is refused with ErrPageSizeChanged, any other
// misaligned write with ErrUnalignedIO. A write past the end extends the
// page count.
func (t *Txn) WriteAt(p []byte, off int64) (int, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	if t.store.writer != nil && !t.reserved {
		return 0, ErrNotReserved
	}
	if off < 0 {
		return 0, fmt.Errorf("sqlitevfs: negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > int(MaxPageSize) {
		return 0, fmt.Errorf("%w: write of %d bytes", ErrUnalignedIO, len(p))
	}
	n := uint32(len(p)) //nolint:gosec // G115: len(p) <= MaxPageSize
	if t.meta.PageSize == 0 {
		if !ValidPageSize(n) {
			return 0, fmt.Errorf("%w: page size %d is not a power of two in %d..%d", ErrInvalidMeta, n, MinPageSize, MaxPageSize)
		}
		if off%int64(n) != 0 {
			return 0, fmt.Errorf("%w: offset %d is not a multiple of page size %d", ErrUnalignedIO, off, n)
		}
		t.meta.PageSize = n
	}
	pageSize := int64(t.meta.PageSize)
	if n != t.meta.PageSize {
		if ValidPageSize(n) {
			return 0, fmt.Errorf("%w: write of %d bytes, page size is %d", ErrPageSizeChanged, n, pageSize)
		}
		return 0, fmt.Errorf("%w: write of %d bytes, page size is %d", ErrUnalignedIO, n, pageSize)
	}
	if off%pageSize != 0 {
		return 0, fmt.Errorf("%w: offset %d, page size is %d", ErrUnalignedIO, off, pageSize)
	}
	idx := off / pageSize
	if idx >= math.MaxUint32 {
		return 0, fmt.Errorf("%w: offset %d is past the last page", ErrInvalidPageNumber, off)
	}
	pgno := uint32(idx) + 1 //nolint:gosec // G115: idx < MaxUint32
	if _, ok := t.dirty[pgno]; ok {
		t.rewrote = true
	}
	t.dirty[pgno] = append([]byte(nil), p...)
	if pgno > t.meta.PageCount {
		t.meta.PageCount = pgno
	}
	t.wrote = true
	return len(p), nil
}

// Truncate sets the file size to size, which must be page-aligned
// (ErrUnalignedIO). Pages past the new end are dropped from the buffer and
// deleted at commit; growing the file adds zero pages.
func (t *Txn) Truncate(size int64) error {
	if t.done {
		return ErrTxnDone
	}
	if t.store.writer != nil && !t.reserved {
		return ErrNotReserved
	}
	if size < 0 {
		return fmt.Errorf("sqlitevfs: negative size %d", size)
	}
	if t.meta.PageSize == 0 {
		if size != 0 {
			return fmt.Errorf("%w: size %d with no page size set", ErrUnalignedIO, size)
		}
		return nil
	}
	pageSize := int64(t.meta.PageSize)
	if size%pageSize != 0 {
		return fmt.Errorf("%w: size %d, page size is %d", ErrUnalignedIO, size, pageSize)
	}
	if size/pageSize > math.MaxUint32 {
		return fmt.Errorf("%w: size %d is past the last page", ErrInvalidPageNumber, size)
	}
	count := uint32(size / pageSize) //nolint:gosec // G115: bounded above
	if count < t.meta.PageCount {
		t.rewrote = true
	}
	for pgno := range t.dirty {
		if pgno > count {
			delete(t.dirty, pgno)
		}
	}
	if count < t.floor {
		t.floor = count
	}
	t.meta.PageCount = count
	t.wrote = true
	return nil
}

// Commit sends the buffered pages, the deletes of the pages the transaction
// truncated away, and the new meta record in one Backend.Commit, and returns
// its revision. A transaction with no writes commits nothing and returns the
// pinned revision; so does one whose writes leave the file as the snapshot
// had it: once a page was written twice or the file shrank, the dirty pages equal to the
// snapshot's copies are dropped (dropUnchanged), and with none left, the
// same page size and count and no page truncated away, nothing is written. The transaction is done afterwards, whether or not the
// commit succeeded. A held writer lock is released afterwards, whether or not
// the commit succeeded, with the same ctx; a Release error is ignored (after
// a successful commit the data is committed and the lease expires on its
// own; after a failed one the commit error is the one to report).
//
// Commit first confirms, on a Store with a writer lock, that this
// transaction still holds it: a lost lease is ErrBusy and nothing is
// written. It then re-reads the meta record at the backend's current
// revision: if its generation differs from the one this transaction pinned,
// the database was restored underneath it (Q4 A), and outside optimistic
// mode a moved change counter means another writer committed since the
// snapshot; either way Commit returns ErrSnapshotChanged and writes nothing. RestoreToRevision takes the lock
// too; BumpGeneration does not: a bump racing a commit is caught at the next
// Begin.
//
// In every mode the meta record Commit re-read is a read check of the write
// itself, so a commit landing between that read and the write, from a
// writer with or without the lock, is ErrSnapshotChanged too. In optimistic mode (WithOptimistic) the write also
// carries every page the transaction read from its snapshot, present or
// absent: a page another writer changed or created since the snapshot is
// ErrSnapshotChanged and nothing is written; a moved page count or page size
// is too, and otherwise the change counter advances from the newest record.
//
// When the dirty pages total more than the Store's stage threshold, or the
// request encoded with its keys, read checks and framing would reach the
// transport request cap, the pages go through staging and one publish (Q2
// A, commitStaged) instead.
// Every read and write Commit makes uses its own ctx, not Begin's.
func (t *Txn) Commit(ctx context.Context) (int64, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	t.done = true
	rev, err := t.commitWrites(ctx)
	_ = t.release(ctx)
	return rev, err
}

// commitWrites is Commit's write path; the caller releases the writer lock.
func (t *Txn) commitWrites(ctx context.Context) (int64, error) {
	if !t.wrote {
		return t.rev, nil
	}
	if t.rewrote {
		if err := t.dropUnchanged(ctx); err != nil {
			return 0, err
		}
	}
	if !t.remove && len(t.stageDeletes) == 0 && len(t.dirty) == 0 && t.floor >= t.base.PageCount && t.meta.PageCount == t.base.PageCount && t.meta.PageSize == t.base.PageSize {
		// Every write was undone (a journal played back over its own
		// writes): nothing to commit, no revision.
		return t.rev, nil
	}
	if t.held {
		if err := t.store.writer.Check(ctx, t.store.lockKey, t.token); err != nil {
			return 0, err
		}
	}
	now, err := t.store.backend.Revision(ctx)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit %q: %w", t.store.db, err)
	}
	current, metaFound, err := t.store.readMeta(ctx, now)
	if err != nil {
		return 0, err
	}
	if current.Generation != t.base.Generation {
		return 0, fmt.Errorf("%w: %q moved from generation %d to %d", ErrSnapshotChanged, t.store.db, t.base.Generation, current.Generation)
	}
	if t.baseFound && !metaFound {
		// A restore removed the database the snapshot saw.
		return 0, fmt.Errorf("%w: %q was removed", ErrSnapshotChanged, t.store.db)
	}
	if !t.store.optimistic && current.ChangeCounter != t.base.ChangeCounter {
		return 0, fmt.Errorf("%w: %q moved from change counter %d to %d", ErrSnapshotChanged, t.store.db, t.base.ChangeCounter, current.ChangeCounter)
	}
	counter := t.base.ChangeCounter
	if t.store.optimistic {
		// Disjoint writers may commit in any order, so the change
		// counter advances from the newest record; a moved page size or
		// page count is a moved file this snapshot cannot merge into.
		if current.PageSize != t.base.PageSize || current.PageCount != t.base.PageCount {
			return 0, fmt.Errorf("%w: %q moved from %d pages of %d bytes to %d pages of %d bytes",
				ErrSnapshotChanged, t.store.db, t.base.PageCount, t.base.PageSize, current.PageCount, current.PageSize)
		}
		counter = current.ChangeCounter
	}
	reads := t.readSet(now, metaFound)
	if t.remove {
		return t.commitRemoval(ctx, now, reads)
	}
	t.meta.Generation = t.base.Generation
	if t.restoreGen != 0 {
		t.meta.Generation = t.restoreGen
	}
	if !metaFound {
		// Every generation is below the revision that committed it, so a
		// record created at now is past every earlier record of this name.
		t.meta.Generation = max(t.meta.Generation, uint64(now)) //nolint:gosec // G115: a revision is never negative
	}
	t.meta.ChangeCounter = counter + 1
	metaValue, err := t.meta.MarshalBinary()
	if err != nil {
		return 0, err
	}
	// Every page past the floor that this commit does not write reads as
	// zeros to this transaction, so its key goes. A page inside the new
	// page count (a hole, possibly over a key a trimAfter left) goes with
	// the commit; one past it, and every stale stage key, is unreachable
	// under the new record and may follow in trimAfter.
	var deletes, trim [][]byte
	for pgno := uint64(t.floor) + 1; pgno <= uint64(max(t.base.PageCount, t.meta.PageCount)); pgno++ {
		if _, ok := t.dirty[uint32(pgno)]; ok { //nolint:gosec // G115: pgno <= a page count, a uint32
			continue
		}
		key, err := PageKey(t.store.db, uint32(pgno)) //nolint:gosec // G115: as above
		if err != nil {
			return 0, err
		}
		if pgno > uint64(t.meta.PageCount) {
			trim = append(trim, key)
		} else {
			deletes = append(deletes, key)
		}
	}
	trim = append(trim, t.stageDeletes...)
	dirtyBytes := 0
	for _, page := range t.dirty {
		dirtyBytes += len(page)
	}
	if dirtyBytes > t.store.stageThreshold {
		return t.commitStaged(ctx, reads, metaValue, deletes, trim)
	}
	puts := make(map[string][]byte, len(t.dirty)+1)
	puts[string(t.store.metaKey)] = metaValue
	for pgno, page := range t.dirty {
		key, err := PageKey(t.store.db, pgno)
		if err != nil {
			return 0, err
		}
		puts[string(key)] = page
	}
	spent := encodedRequestBytes(reads, puts, deletes)
	if len(t.dirty) > 0 && spent >= t.store.requestBytes {
		// The pages fit the threshold but the request, encoded with its
		// keys, read checks and framing, would reach the transport cap.
		return t.commitStaged(ctx, reads, metaValue, deletes, trim)
	}
	head, rest := t.store.splitDeletes(spent, trim)
	rev, err := t.store.backend.Commit(ctx, reads, puts, append(deletes, head...))
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit %q: %w", t.store.db, err)
	}
	t.store.trimAfter(ctx, rev, rest)
	return rev, nil
}

// dropUnchanged drops from the write set every dirty page whose bytes equal
// the snapshot's copy at rev (an absent page within the snapshot's page
// count reads as zeros), so a journal played back over the transaction's
// own writes leaves nothing to commit. Pages past the snapshot (or past a
// truncation floor) are new and kept. Reads use ctx, Commit's.
func (t *Txn) dropUnchanged(ctx context.Context) error {
	if t.meta.PageSize != t.base.PageSize {
		return nil
	}
	for pgno, page := range t.dirty {
		if pgno > t.floor || pgno > t.base.PageCount {
			continue
		}
		key, err := PageKey(t.store.db, pgno)
		if err != nil {
			return err
		}
		value, found, err := t.store.backend.GetAt(ctx, key, t.rev)
		if err != nil {
			return fmt.Errorf("sqlitevfs: commit %q: read page %d at revision %d: %w", t.store.db, pgno, t.rev, err)
		}
		if !found {
			value = make([]byte, len(page))
		}
		if bytes.Equal(value, page) {
			delete(t.dirty, pgno)
		}
	}
	return nil
}

// readSet returns the read checks a commit carries. In every mode the meta
// record read at revision now (found or not) must be unchanged at the write,
// which makes Commit's generation and change counter checks (and, in
// optimistic mode, its file size check) atomic with it, so a writer that
// does not take the writer lock cannot slip a commit in under one that
// does. In optimistic mode every page the transaction read from its snapshot
// must also be unchanged since rev, in key order.
func (t *Txn) readSet(now int64, metaFound bool) []ReadCheck {
	reads := make([]ReadCheck, 0, len(t.reads)+1)
	reads = append(reads, ReadCheck{Key: t.store.metaKey, Rev: now, Found: metaFound})
	keys := make([]string, 0, len(t.reads))
	for key := range t.reads {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reads = append(reads, ReadCheck{Key: []byte(key), Rev: t.rev, Found: t.reads[key]})
	}
	return reads
}

// Rollback discards the buffer and releases a held writer lock. The
// transaction is done afterwards.
func (t *Txn) Rollback(ctx context.Context) error {
	if t.done {
		return ErrTxnDone
	}
	t.done = true
	t.dirty = nil
	return t.release(ctx)
}
