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

package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// rotateRetryInterval bounds how often a FAILED rotation is re-attempted.
//
// Rotation failure is an operator-resolvable condition -- a permission change,
// a full disk, a directory that became unwritable while the open handle stayed
// writable. Retrying it on every event turns that one fault into a
// chain-anchor line per event and a full-file rescan per event, under the
// writer's lock. A minute is short enough that a fixed condition resumes
// rotation promptly and long enough that the cost of the fault is bounded.
const rotateRetryInterval = time.Minute

// rotationFailures counts rotations that could not complete.
//
// SEPARATE FROM qrdb_audit_write_failures_total ON PURPOSE. That counter means
// "a record was lost"; this one means "the size bound is not being honoured
// and every record is still landing". Folding them together would make the
// first one lie, and it is the one an operator pages on.
var rotationFailures = sync.OnceValue(func() *metrics.Counter {
	return metrics.NewCounter(metrics.CounterOpts{
		Namespace: "qrdb",
		Subsystem: "audit",
		Name:      "rotation_failures_total",
		Help: "Audit log rotations that could not complete. Records continue to be " +
			"written; the file has stopped honouring its size bound.",
	})
})

// ChainFileOption configures a ChainFileLogger.
type ChainFileOption func(*ChainFileLogger)

// WithMaxBytes sets the maximum file size before rotation.
// Default: 100 MiB.
func WithMaxBytes(n int64) ChainFileOption {
	return func(l *ChainFileLogger) { l.maxBytes = n }
}

// WithRetentionDays sets the number of days to retain rotated files.
// Files older than this are deleted on startup. Default: 90.
func WithRetentionDays(days int) ChainFileOption {
	return func(l *ChainFileLogger) { l.retentionDays = days }
}

// WithSyncPerWrite enables fsync after every write (compliance mode).
// Default: true.
func WithSyncPerWrite(v bool) ChainFileOption {
	return func(l *ChainFileLogger) { l.syncPerWrite = v }
}

// ChainFileLogger is an append-only NDJSON file logger with SHA-256
// hash chaining for tamper detection, daily rotation, configurable
// maximum file size, and configurable retention policy.
//
// Hash-chain design:
//   - prevDigest starts as 32 zero bytes for the first event of the TRAIL.
//   - For each event: canonicalJSON = MarshalCanonical(event_without_prevHash);
//     currentDigest = SHA-256(prevDigest || canonicalJSON).
//   - event.PrevHash = hex(prevDigest) written to the NDJSON line, empty when
//     prevDigest is zero.
//   - prevDigest advances to currentDigest.
//
// Rotation boundary -- THE CHAIN SPANS IT, it does not restart per file
// (go-qrdb#452; the reasoning is at the top of chain_verify.go):
//   - When the file would exceed maxBytes, the logger rotates to a new file
//     named audit-YYYY-MM-DD-NNN.ndjson. A "chain anchor" entry is appended to
//     the outgoing file recording the handoff prevHash.
//   - The first record of the new file carries that same handoff digest as its
//     PrevHash, so the anchor and its successor's first record state the same
//     value from either side of the boundary. verifyChainSet follows it.
//   - The anchor is NOT folded into prevDigest: it terminates the outgoing
//     file after the handoff was fixed. A verifier folds it like any other
//     record (a record that follows one -- see #268 -- chains over it) and
//     takes the handoff from the digest standing BEFORE a terminal anchor.
type ChainFileLogger struct {
	path          string
	maxBytes      int64
	retentionDays int
	syncPerWrite  bool

	mu         sync.Mutex
	f          *os.File
	prevDigest [sha256.Size]byte
	size       int64

	// rotations is a sequence counter the verifier reads WITHOUT l.mu. rotate
	// makes it odd before the rename and even again once the replacement
	// file is open, so a verifier that reads the same even value on both
	// sides of a directory listing knows no rotation was mid-stride while it
	// listed. Without it a listing taken in the gap between the rename and
	// the re-open -- the active name briefly does not exist -- could not be
	// told from an unlinked audit log. See verifyChainSetAt.
	rotations atomic.Uint64

	// writeFunc, syncFunc and rotateFunc, when non-nil, replace the append,
	// the fsync and the rotation.
	//
	// They exist because an fsync failure and a rotation failure cannot be
	// induced portably from a test -- a full disk, an I/O error and a
	// permission-denied rename are properties of the filesystem, and the
	// chmod trick that fakes the last one is a no-op under a root CI runner.
	// Each of those branches is security-relevant: each is one of the things
	// that makes the authorization middleware refuse a request it could not
	// record (go-qrdb#335), and a branch nothing exercises is how *WriteError
	// ended up on go-qrdb#243's never-constructed list in the first place.
	//
	// Unexported, with no constructor and no option -- only a test in THIS
	// package can set them, and nothing in cmd/, pkg/ or sdk/ does.
	//
	// The middleware's own end-to-end proof does NOT use this seam. It closes
	// a real ChainFileLogger and emits, which is a real production race (the
	// daemon closes the sink while requests drain) and fails at the real
	// boundary, os.File.Write. Read and written under l.mu with every other
	// field.
	//
	// WHAT rotateFunc DOES NOT COVER, and this cost a real defect. It replaces
	// rotate() WHOLESALE, so a test using it exercises none of rotate's own
	// failure modes -- not the anchor write, not Close, not the rename, not
	// the reopen. TestChainFileLogger_Emit_PersistentRotationFailure_
	// DoesNotWedgeTheNode asserted "does not wedge the node" through this
	// seam, and that property held for a failed RENAME while being false for a
	// failed CLOSE, which the double had silently removed from the test.
	//
	// That is the general hazard of wholesale-replacement fault injection: the
	// seam that makes a step testable makes the step's own internals
	// untestable, and the test name goes on claiming the property. Anything
	// asserting a property OF rotate must drive the real rotate() -- see
	// TestChainFileLogger_Rotation_CloseFailureDoesNotWedgeTheNode, which
	// pre-closes the handle instead.
	//
	// renameFunc is the NARROW one, and it is narrow on purpose: it replaces
	// only the rename inside rotate(), so the anchor write, the close, the
	// reopen and the failure accounting all still run for real. It exists
	// because os.Rename failing while the open handle stays writable -- an
	// unwritable directory from a hardening pass, a bind-mount, an ACL change,
	// the scenario rotateRetryInterval was written for -- leaves the anchor in
	// the file the next append extends, and that is the one rotation failure
	// with a consequence for the CHAIN rather than only for the size bound.
	// The chmod that would fake it is a no-op under a root CI runner.
	writeFunc  func([]byte) (int, error)
	syncFunc   func() error
	rotateFunc func() error
	renameFunc func(oldpath, newpath string) error

	// closed records that the OWNER closed this logger, as distinct from the
	// logger having lost its handle to a failed rotation. ensureOpen reopens
	// the second and refuses the first: a Close()d sink must stay closed, not
	// resurrect itself on the next write.
	closed bool

	// rotateRetryAt is the earliest time a failed rotation may be re-attempted.
	// Zero means "no failure outstanding". See tryRotate for why an unbounded
	// retry is an outage of its own. Read and written under l.mu.
	rotateRetryAt time.Time
}

// NewFileLogger opens the audit log file at path and returns a ChainFileLogger.
// On startup, rotated files older than retentionDays are deleted. The active
// log file is opened for append; the prevDigest is initialised by scanning
// the tail of the file.
func NewFileLogger(path string, opts ...ChainFileOption) (*ChainFileLogger, error) {
	l := &ChainFileLogger{
		path:          path,
		maxBytes:      100 * 1024 * 1024, // 100 MiB
		retentionDays: 90,
		syncPerWrite:  true,
	}
	for _, opt := range opts {
		opt(l)
	}

	if err := l.pruneOldFiles(); err != nil {
		slog.Error("audit: retention pruning failed", "error", err)
		// Non-fatal: log and continue.
	}

	if err := l.openFile(); err != nil {
		return nil, err
	}

	return l, nil
}

// openFile opens (or creates) the active audit log file and initialises
// prevDigest from the last event in the file.
func (l *ChainFileLogger) openFile() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		// Leave l.f NIL rather than whatever it was. A failure here used to
		// leave the previous, already-closed handle in place, which every
		// later write then failed against forever -- and because rotate()
		// resets l.size before calling this, tryRotate never fired again
		// either, so nothing ever called openFile a second time. A dead sink
		// with no retry path at all. ensureOpen restores it on the next Emit.
		l.f = nil
		return &FileError{Path: l.path, Err: err}
	}
	l.f = f

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		l.f = nil
		return &FileError{Path: l.path, Err: err}
	}
	l.size = info.Size()

	// Initialise prevDigest from the last written event.
	if l.size > 0 {
		if err := l.initPrevDigest(f); err != nil {
			// Non-fatal: chain will restart from zero on next write.
			slog.Error("audit: could not restore chain digest", "path", l.path, "error", err)
		}
	}

	return nil
}

// initPrevDigest scans the file and re-derives prevDigest as
// SHA-256(prevDigest||canonicalJSON(event)) for each event it can read back,
// leaving prevDigest pointing at the digest after the last one.
//
// IT SEEDS FROM THE FIRST RECORD'S STORED PrevHash, not from zero, and that
// distinction is the whole reason a restart used to break the chain. The chain
// spans rotations (go-qrdb#452), so a file opened after one begins mid-chain:
// folding it from zero produced a digest no record in the file agrees with,
// and the next event written after the restart linked to it. VerifyChain then
// reported a break at index 1 on honest data -- a second face of the same
// defect, reached by restarting rather than by rotating.
//
// What has NOT changed is the property the comment on Emit's write depends on:
// this never reads the LAST record's stored PrevHash. It folds over the
// records it can actually read back, so a torn or unsynced tail leaves the
// digest where the surviving records put it. Only the starting point is taken
// from the file, and only from the one record whose predecessor is not in this
// file to be folded. verifyChainSegment seeds itself the identical way, which
// is what keeps the two readers in step.
func (l *ChainFileLogger) initPrevDigest(f *os.File) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	defer func() { _, _ = f.Seek(0, 2) }() // reset to end

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLine)
	var prev [sha256.Size]byte
	seeded := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		canonical, err := canonicalJSON(ev)
		if err != nil {
			continue
		}
		if !seeded {
			seed, ok := decodeDigest(ev.PrevHash)
			if !ok {
				// An unparsable seed is a tampered or corrupt head. Folding
				// from zero instead would silently write a chain the file
				// cannot support; refusing leaves prevDigest untouched and
				// openFile logs it.
				return &ChainBrokenError{
					Path:     l.path,
					Expected: "hex-encoded sha-256 digest",
					Got:      ev.PrevHash,
				}
			}
			prev = seed
			seeded = true
		}
		prev = chainDigest(prev, canonical)
	}
	if err := scanner.Err(); err != nil {
		return &FileError{Path: l.path, Err: err}
	}
	l.prevDigest = prev
	return nil
}

// Emit appends the event to the NDJSON file with an updated hash chain and
// reports whether it was durably recorded.
//
// It returns nil only when the line was written AND -- with syncPerWrite, the
// default -- fsynced, and the logger is still maintaining its size bound.
// Every failure returns a *WriteError naming this file as the backend, so a
// caller that treats the record as part of its operation (see
// pkg/auth/middleware) can refuse rather than proceed unrecorded.
//
// Ordering of the failure paths is deliberate:
//
//   - A marshal failure returns IMMEDIATELY. No line exists, so there is
//     nothing to salvage by continuing.
//   - A ROTATION failure is NOT REPORTED HERE AT ALL. Rotation runs before the
//     append; when it fails the append still runs, still fsyncs, and the event
//     is on disk exactly as it would have been. The record is durable, so this
//     returns nil -- reporting it would make a caller refuse a request whose
//     audit entry had in fact persisted. The failure is loud on slog and on
//     qrdb_audit_rotation_failures_total instead. See tryRotate.
//
// THE RETURNED ERROR DESCRIBES WHETHER THE RECORD IS DURABLE, NOT WHETHER
// EVERY HOUSEKEEPING STEP SUCCEEDED. That cuts both ways and both directions
// have been defects here: returning an error never skips advancing the hash
// chain and the size over bytes that reached the file (see the comment at the
// write below), and a step that does not bear on durability -- rotation --
// never produces an error at all.
func (l *ChainFileLogger) Emit(_ context.Context, ev AuditEvent) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Restore a handle a failed rotation could not reopen, before anything
	// else touches the file. Cheap: one nil check on every healthy write.
	if err := l.ensureOpen(); err != nil {
		return &WriteError{Backend: l.path, Err: err}
	}

	// Compute canonical JSON without PrevHash, then derive the new digest.
	canonical, err := canonicalJSON(ev)
	if err != nil {
		slog.Error("audit: cannot marshal event for chain", "error", err)
		return &WriteError{Backend: l.path, Err: err}
	}
	prevAtMarshal := l.prevDigest
	newDigest := chainDigest(prevAtMarshal, canonical)

	// Set PrevHash to the previous digest (what the reader uses to verify).
	ev.PrevHash = expectedPrevHash(prevAtMarshal)

	line, err := json.Marshal(ev)
	if err != nil {
		slog.Error("audit: cannot marshal event", "error", err)
		return &WriteError{Backend: l.path, Err: err}
	}
	line = append(line, '\n')

	// Rotate if the file would exceed maxBytes. A failure here is reported on
	// its own channels and is NOT part of this Emit's answer -- see tryRotate.
	if l.size+int64(len(line)) > l.maxBytes {
		l.tryRotate()
	}

	// A ROTATION THAT DID NOT MOVE THE FILE AWAY MOVED THE CHAIN UNDER THIS
	// RECORD, and the record has to be re-fixed to the digest the file it is
	// about to land in actually expects.
	//
	// The case is a FAILED RENAME. rotate() appended the chain anchor before
	// renaming, so when the rename fails the anchor is durable in the very
	// file this append is about to extend, and openFile has re-folded that
	// file INCLUDING the anchor. The line marshalled above still carries the
	// pre-anchor digest, so appending it as-is puts a record after the anchor
	// claiming the digest from before it -- a break at that record, on
	// entirely honest data, from a recoverable fault.
	//
	// A SUCCESSFUL rotation deliberately does NOT take this branch: it leaves
	// prevDigest at the handoff, which is what the first record of the new
	// file must carry for the chain to span the boundary (go-qrdb#452).
	//
	// So the comparison is the whole condition, and on the healthy path it is
	// one 32-byte compare that is always equal -- no second marshal, which is
	// the cost this design exists to avoid paying on every write.
	if l.prevDigest != prevAtMarshal {
		newDigest = chainDigest(l.prevDigest, canonical)
		ev.PrevHash = expectedPrevHash(l.prevDigest)
		remarshalled, err := json.Marshal(ev)
		if err != nil {
			slog.Error("audit: cannot re-marshal event after a failed rotation", "error", err)
			return &WriteError{Backend: l.path, Err: err}
		}
		line = append(remarshalled, '\n')
	}

	n, writeErr := l.write(line)

	// THE CHAIN AND THE SIZE DESCRIBE THE BYTES THAT ARE IN THE FILE, not the
	// bytes the write reported success for. Both advance HERE, before any
	// error is returned, and the error is returned afterwards.
	//
	// Getting this backwards is a durable-state bug, not a reporting one. An
	// fsync failure arrives AFTER the append has already put the record in the
	// file; returning early without advancing prevDigest left the next event
	// linking to the pre-failure digest, so VerifyChain reported
	// ChainBrokenError on honest, untampered data -- the sink the middleware
	// is being told to trust would have failed read-back verification of its
	// own trail under the one fault this package injects.
	//
	// n == len(line) is the condition, not "the write returned nil": a short
	// write leaves a TORN line, which is not a record. initPrevDigest and
	// verifyChainFile both skip a line that will not unmarshal, so leaving
	// prevDigest where it is keeps them in step. Size advances by n either
	// way, because those bytes really are occupying the file against maxBytes.
	//
	// Truncating back to the pre-write offset was the alternative and is
	// worse: it is a second I/O on a disk that has just failed one, it
	// destroys a record that genuinely landed, and a crash before it runs
	// leaves exactly the inconsistency it was meant to prevent.
	//
	// The crash case needs no special handling, because initPrevDigest
	// re-derives the digest by folding over THE RECORDS IT CAN READ BACK
	// rather than trusting the last record's stored PrevHash. If the unsynced
	// record survived, the fold includes it and the next write links after it;
	// if it did not, the fold lands on the pre-failure digest and the next
	// write links after the last record that did survive. Both are consistent
	// with the file as it exists, in both directions.
	l.size += int64(n)
	if n == len(line) {
		l.prevDigest = newDigest
	}

	if writeErr != nil {
		slog.Error("audit: write failed", "path", l.path, "error", writeErr)
		return &WriteError{Backend: l.path, Err: writeErr}
	}

	if l.syncPerWrite {
		if err := l.sync(); err != nil {
			slog.Error("audit: fsync failed", "path", l.path, "error", err)
			return &WriteError{Backend: l.path, Err: err}
		}
	}

	return nil
}

// tryRotate rotates the active file and absorbs a failure.
//
// A ROTATION FAILURE IS NOT AN UNWRITTEN RECORD, and Emit must not report it
// as one. The rename happens BEFORE the append; when it fails the append still
// runs, still fsyncs, and the event is on disk exactly as it would have been.
// Returning *WriteError anyway made the middleware refuse a request whose
// audit record had in fact persisted -- contradicting the contract this
// package states on AuditLogger.Emit, that a nil return means the event
// reached the sink's durability boundary. It did.
//
// The consequence was not one spurious refusal. rotate() reopens
// unconditionally and openFile re-stats the same oversized file, so l.size
// stays above maxBytes and EVERY subsequent Emit re-attempts the rotation. A
// directory that has become unwritable while the open handle stays writable --
// a hardening pass, a bind-mount, an ACL change -- therefore took the node to
// 503-on-every-authorized-request, including the tenant-barrier unseal an
// operator would use to recover, while the trail stayed perfectly healthy.
// A self-inflicted outage from a condition that does not threaten the property
// fail-closed protects.
//
// It is still LOUD, on two channels that do not refuse anybody: an slog.Error
// and qrdb_audit_rotation_failures_total. A logger that cannot rotate has
// stopped honouring maxBytes and will eventually fail to write for real -- an
// operator must learn that before it does.
//
// Must be called with l.mu held.
func (l *ChainFileLogger) tryRotate() {
	// A failed rotation is re-attempted at most once per rotateRetryInterval.
	// Without the bound, every Emit past the size limit appends another
	// chain-anchor line, closes and reopens the file, and re-scans it end to
	// end in initPrevDigest -- up to maxBytes, under this lock, per event.
	// Turning one operator-resolvable fault into unbounded work and an
	// anchor-per-event is its own outage, just a slower one than the 503.
	if !l.rotateRetryAt.IsZero() && time.Now().Before(l.rotateRetryAt) {
		return
	}

	if err := l.maybeRotate(); err != nil {
		l.rotateRetryAt = time.Now().Add(rotateRetryInterval)
		rotationFailures().Inc()
		slog.Error("audit: rotation failed; the size bound is not being honoured",
			"path", l.path,
			"size", l.size,
			"max_bytes", l.maxBytes,
			"retry_after", rotateRetryInterval,
			"error", err,
		)
		return
	}
	l.rotateRetryAt = time.Time{}
}

// ensureOpen restores the file handle when a previous rotation could not.
//
// A rotation closes the outgoing file and reopens a fresh one. When that
// reopen fails -- a full disk refusing a new inode, a directory that became
// unwritable -- the logger is left with no file, and before this existed there
// was NO PATH BACK: openFile has only two callers, NewFileLogger and rotate,
// and rotate resets l.size to 0 on its way past, so the size check never fired
// again and rotate was never called again either. One transient failure killed
// the sink until the process restarted, which under the fail-closed middleware
// means 503 on every authorized request forever.
//
// It deliberately does NOT resurrect a logger the OWNER closed. Close() means
// closed; a sink that reopened itself after Close would keep writing during
// shutdown and would make "the handle is gone" untestable.
//
// Must be called with l.mu held.
func (l *ChainFileLogger) ensureOpen() error {
	if l.closed {
		return os.ErrClosed
	}
	if l.f != nil {
		return nil
	}
	return l.openFile()
}

// maybeRotate rotates the active file, through the rotateFunc seam when one
// is installed. Must be called with l.mu held.
func (l *ChainFileLogger) maybeRotate() error {
	if l.rotateFunc != nil {
		return l.rotateFunc()
	}
	return l.rotate()
}

// write appends p to the active file, through the writeFunc seam when one is
// installed. Must be called with l.mu held.
func (l *ChainFileLogger) write(p []byte) (int, error) {
	if l.writeFunc != nil {
		return l.writeFunc(p)
	}
	return l.f.Write(p)
}

// sync flushes the active file to stable storage, through the syncFunc seam
// when one is installed. Must be called with l.mu held.
func (l *ChainFileLogger) sync() error {
	if l.syncFunc != nil {
		return l.syncFunc()
	}
	return l.f.Sync()
}

// rename commits the rotation, through the renameFunc seam when one is
// installed. Must be called with l.mu held.
func (l *ChainFileLogger) rename(oldpath, newpath string) error {
	if l.renameFunc != nil {
		return l.renameFunc(oldpath, newpath)
	}
	return fsync.Rename(oldpath, newpath)
}

// Query scans the active file and returns events matching the filter.
// It opens a separate read-only file handle to avoid locking the writer.
func (l *ChainFileLogger) Query(_ context.Context, f Filter) (*Page, error) {
	f.Limit = normalizeLimit(f.Limit)

	l.mu.Lock()
	path := l.path
	l.mu.Unlock()

	events, err := scanFile(path, f)
	if err != nil {
		return nil, err
	}

	return buildPage(events, f), nil
}

// VerifyChain verifies the WHOLE ROTATION SET -- the active file and every
// archive beside it -- and returns an error describing the first fault.
//
// It verifies the set rather than one file because the chain spans rotations
// (go-qrdb#452). Checking only the active file would have to either ignore the
// archives, which is most of the trail, or report a break at index 0 on every
// file opened after a rotation, which is the defect this replaces. The
// distinction the set walk buys is real: a *ChainBrokenError means a RECORD
// was changed, a *ChainLinkError means a FILE was.
//
// Use VerifyChainSet when the answer matters beyond pass/fail -- in particular
// to learn whether the walk reached the chain's origin. See ChainVerification.
func (l *ChainFileLogger) VerifyChain(ctx context.Context) error {
	_, err := l.VerifyChainSet(ctx)
	return err
}

// VerifyChainSet verifies the rotation set and reports what it covered.
//
// The result matters because a clean verification is not necessarily a
// complete one: retention pruning deletes the oldest archives, so a healthy
// trail's walk routinely begins mid-chain, and Genesis == false is the only
// way a caller learns that the digests were not traced back to zero. Removing
// a contiguous prefix of the archives is the one edit a hash walk cannot tell
// from retention doing its job; every other edit breaks a link.
//
// It takes no lock: l.path never changes after construction, and the walk
// learns whether a rotation overlapped it from l.rotations.
func (l *ChainFileLogger) VerifyChainSet(_ context.Context) (*ChainVerification, error) {
	return verifyChainSetAt(l.path, l.rotations.Load)
}

// Close flushes and closes the underlying file.
func (l *ChainFileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		l.closed = true
		return nil
	}
	err := l.f.Close()
	l.f = nil
	l.closed = true
	return err
}

// rotate moves the current file to a timestamped archive name, appends
// a chain-anchor entry to the old file, and opens a fresh file.
// Must be called with l.mu held.
func (l *ChainFileLogger) rotate() error {
	// Append chain anchor to the old file before rotating.
	//
	// PrevHash follows Emit's rule EXACTLY -- a zero digest is written empty,
	// never as sixty-four zeros -- because a verifier cannot have two rules
	// for the same field. It reaches here zero only when the file is rotated
	// before it holds a foldable record, which maxBytes below one line's
	// length makes possible.
	anchor := AuditEvent{
		Timestamp: time.Now().UTC(),
		Actor:     chainAnchorActor,
		Resource:  chainAnchorResource,
		Action:    chainAnchorAction,
		Outcome:   OutcomeSuccess,
		PrevHash:  expectedPrevHash(l.prevDigest),
		Attributes: map[string]any{
			"note": "rotation boundary; the chain CONTINUES into the next file, whose first " +
				"record carries this same prevHash; this entry is the handoff from this side",
		},
	}
	anchorLine, anchorErr := json.Marshal(anchor)
	if anchorErr != nil {
		// Practically unreachable -- every field is marshalable -- but the
		// anchor is the only record of the cross-file chain handoff, so its
		// loss is stated rather than discarded.
		slog.Error("audit: cannot marshal chain anchor", "path", l.path, "error", anchorErr)
	}
	anchorLine = append(anchorLine, '\n')
	if _, err := l.f.Write(anchorLine); err != nil {
		slog.Error("audit: chain anchor not written; the cross-file chain handoff for this "+
			"rotation is lost", "path", l.path, "error", err)
	}
	if err := l.f.Sync(); err != nil {
		slog.Error("audit: chain anchor not durable", "path", l.path, "error", err)
	}

	// CLOSE IS CAPTURED, NEVER RETURNED EARLY. os.File.Close releases the
	// descriptor whether or not close(2) reported an error, so by this line the
	// handle is gone in either case -- and returning here skipped the
	// unconditional reopen below, whose own comment says exactly why it must
	// not be skipped.
	//
	// The consequence was the worst failure this sink had. l.size is reset
	// below, so an early return also left it oversized: every later Emit
	// re-entered tryRotate, rotate() wrote the anchor to a closed file and
	// returned here again, and openFile -- which has only two callers -- was
	// never reached. With the fail-closed middleware that is 503 on every
	// authorized request including tenant-barrier unseal, PERMANENTLY, not
	// self-healing even after the disk recovers, and the backoff cannot clear
	// it because the retry path itself is what is broken.
	//
	// The trigger is precisely this feature's subject: a deferred write error
	// surfacing at close(2) -- ENOSPC/EDQUOT on NFS, EIO on delayed allocation.
	closeErr := l.f.Close()
	if closeErr != nil {
		slog.Error("audit: closing the outgoing file failed during rotation",
			"path", l.path, "error", closeErr)
	}
	// Nil it so ensureOpen can tell "we have no file" from "we have a dead
	// one", and so a failed openFile below leaves a state the next Emit can
	// recover from rather than a closed handle it will keep writing to.
	l.f = nil

	// Rename to archive, durably. The rename IS the rotation's commit point:
	// after it, the chain-anchor entry that closes the old file lives only
	// under the archive name. A crash that loses the directory entry leaves the
	// anchored file reachable at l.path, where openFile keeps appending to it
	// -- an anchor buried mid-file, a file still over maxBytes, and a rotation
	// that has to happen again (go-qrdb#268).
	//
	// Since go-qrdb#452 that state is at least READABLE: the chain spans
	// rotations, initPrevDigest folds the buried anchor, records written after
	// it chain over it, and verifyChainSegment folds it the same way, so the
	// file verifies as the one continuous chain it actually is. The durability
	// of this rename still matters -- the size bound and the archive boundary
	// depend on it -- but it is no longer the difference between a verifiable
	// trail and an unverifiable one.
	archivePath := rotatedPath(l.path)
	// From the rename until the replacement file is open the active name does
	// not exist. Mark that window for the lock-free verifier: odd while open,
	// even again below whatever the rename and the reopen did.
	l.rotations.Add(1)
	rotErr := l.rename(l.path, archivePath)

	// Reset the SIZE for the new file, and deliberately LEAVE prevDigest where
	// it is -- at the handoff this rotation's anchor just recorded.
	//
	// It used to be zeroed here, which was harmless on the happy path because
	// Emit overwrites prevDigest after its append anyway, and wrong on the
	// failure path: after a failed rename openFile re-folds the SAME file and
	// overwrites prevDigest from it, so what this line left behind decided
	// nothing, while a caller reading prevDigest between the two saw a value
	// belonging to no file at all. Leaving the handoff makes prevDigest mean
	// one thing at every point: the digest the next record must carry.
	//
	// That is also what makes the chain span the boundary (go-qrdb#452): when
	// the rename succeeds, openFile finds an EMPTY file, derives nothing, and
	// the handoff stands -- so the first record of the new file carries the
	// digest the outgoing file's anchor states.
	l.size = 0

	// Reopen UNCONDITIONALLY. l.f was closed above, so returning early on a
	// rotation failure would leave the logger with a nil file and every
	// subsequent Emit silently failing to write -- an audit logger that stops
	// auditing on a transient rename or fsync error. Report the rotation
	// failure, but not by disabling the log.
	openErr := l.openFile()
	l.rotations.Add(1)
	if rotErr != nil || closeErr != nil {
		// Join rather than pick: when the reopen fails too, the logger is both
		// undurable AND dead, and reporting only the first hides the second.
		return &FileError{Path: l.path, Err: errors.Join(closeErr, rotErr, openErr)}
	}
	return openErr
}

// pruneOldFiles deletes rotated audit files older than retentionDays.
func (l *ChainFileLogger) pruneOldFiles() error {
	dir := filepath.Dir(l.path)
	base := filepath.Base(l.path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &RetentionError{Err: err}
	}

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)
	for _, e := range entries {
		name := e.Name()
		if name == base {
			continue // skip active file
		}
		if !strings.HasPrefix(name, stem+"-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				slog.Error("audit: failed to prune rotated file", "name", name, "error", err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Package-level helpers
// ---------------------------------------------------------------------------

// rotatedPath returns an archive path derived from path, stamped with the
// current date and a monotonic counter to avoid collisions.
func rotatedPath(path string) string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	stamp := time.Now().UTC().Format("2006-01-02")
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%s-%03d%s", stem, stamp, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	// Fallback: use nanoseconds to avoid collision.
	return fmt.Sprintf("%s-%s-%d%s", stem, stamp, time.Now().UnixNano(), ext)
}

// canonicalJSON serialises ev without the PrevHash field for digest computation.
// The serialisation uses standard encoding/json; field order is determined by
// the struct field order which is stable.
func canonicalJSON(ev AuditEvent) ([]byte, error) {
	// Strip PrevHash for canonical computation.
	type canonical struct {
		Timestamp  time.Time      `json:"timestamp"`
		Actor      string         `json:"actor"`
		TenantID   string         `json:"tenantID,omitempty"`
		Role       string         `json:"role,omitempty"`
		Resource   string         `json:"resource"`
		Action     string         `json:"action"`
		Outcome    string         `json:"outcome"`
		Attributes map[string]any `json:"attributes,omitempty"`
		ClientIP   string         `json:"clientIP,omitempty"`
		RequestID  string         `json:"requestID,omitempty"`
	}
	c := canonical{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Actor,
		TenantID:   ev.TenantID,
		Role:       ev.Role,
		Resource:   ev.Resource,
		Action:     ev.Action,
		Outcome:    ev.Outcome,
		Attributes: ev.Attributes,
		ClientIP:   ev.ClientIP,
		RequestID:  ev.RequestID,
	}
	return json.Marshal(c)
}

// chainDigest computes SHA-256(prev || data).
func chainDigest(prev [sha256.Size]byte, data []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write(prev[:])
	h.Write(data)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// allZero returns true if all bytes in b are zero.
func allZero(b [sha256.Size]byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// scanFile opens the NDJSON file at path and returns all events matching f.
func scanFile(path string, f Filter) ([]AuditEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, &FileError{Path: path, Err: err}
	}
	defer func() { _ = file.Close() }()

	var events []AuditEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if matchesFilter(ev, f) {
			events = append(events, ev)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	return events, nil
}

// matchesFilter returns true if ev satisfies all non-zero filter fields.
func matchesFilter(ev AuditEvent, f Filter) bool {
	if !f.From.IsZero() && ev.Timestamp.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !ev.Timestamp.Before(f.To) {
		return false
	}
	if f.Actor != "" && ev.Actor != f.Actor {
		return false
	}
	if f.Resource != "" && ev.Resource != f.Resource {
		return false
	}
	if f.Action != "" && ev.Action != f.Action {
		return false
	}
	if f.Outcome != "" && !strings.HasPrefix(ev.Outcome, f.Outcome) {
		return false
	}
	if f.TenantID != "" && ev.TenantID != f.TenantID && (!f.Untenanted || ev.TenantID != "") {
		return false
	}
	return true
}

// buildPage applies cursor pagination and constructs a Page from events.
func buildPage(events []AuditEvent, f Filter) *Page {
	// Sort by timestamp ascending for stable pagination.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	// Apply cursor: skip events before the cursor timestamp.
	start := 0
	if f.Cursor != "" {
		for i, ev := range events {
			ts := ev.Timestamp.UTC().Format(time.RFC3339Nano)
			if ts == f.Cursor {
				start = i + 1
				break
			}
		}
	}
	events = events[start:]

	total := len(events)

	limit := normalizeLimit(f.Limit)
	var next string
	if len(events) > limit {
		next = events[limit-1].Timestamp.UTC().Format(time.RFC3339Nano)
		events = events[:limit]
	}

	return &Page{
		Events: events,
		Next:   next,
		Total:  total,
	}
}
