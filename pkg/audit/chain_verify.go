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
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// The chain's contract across a rotation (go-qrdb#452)
//
// THE CHAIN SPANS THE ROTATION. It does not restart per file.
//
// The writer has always produced a spanning chain: Emit derives the outgoing
// record's digest from the digest standing BEFORE the rotation, so the first
// record of a new file genuinely continues the previous file's chain, and the
// chain-anchor rotate() appends to the outgoing file records that handoff
// digest. What was missing was a reader that knew it. verifyChainFile assumed
// every file began at zero, so every file opened after a rotation failed at
// index 0 on entirely honest data -- the signal this subsystem exists to make
// trustworthy, firing on healthy archives.
//
// The alternative was to make each file self-contained: rotate BEFORE the
// record's PrevHash is fixed, so a new file's first record carries none. It
// was rejected, for three reasons.
//
//  1. IT DELETES TAMPER-EVIDENCE THAT THE DATA ALREADY CARRIES. With
//     per-file chains, nothing in file N+1 refers to file N. An adversary who
//     can edit a line can delete a file, and deleting a whole archive would
//     leave a set in which every remaining file verifies perfectly. Spanning,
//     that removal breaks the link between the predecessor's handoff and the
//     successor's seed.
//  2. IT MAKES THE ANCHOR DECORATIVE. Under per-file chains the anchor's
//     PrevHash is derivable from the file it terminates and carries no
//     information. Spanning, it is the handoff -- and it also PINS THE LAST
//     REAL RECORD of every archive, which a self-contained chain cannot do for
//     its own final entry.
//  3. IT COSTS THE WRITE PATH. Rotation fires on the marshalled line's length,
//     so rotating first means marshal-to-size, rotate, recompute the digest,
//     re-marshal -- a second marshal on the sink every authorization decision
//     now goes through (go-qrdb#335). The reader-side fix costs the write path
//     nothing; Emit is untouched by this change.
//
// What is given up: an archive cannot be checked against a global zero on its
// own. That is decomposed rather than lost. verifyChainSegment checks one
// file's INTERNAL integrity against the seed the file itself declares -- which
// catches every modification, insertion, reordering and truncation within the
// file -- and verifyChainSet then checks that each declared seed really is its
// predecessor's handoff, which is what makes the seed unforgeable.
//
// The one thing a chain walk cannot distinguish is the removal of a CONTIGUOUS
// PREFIX of the set from retention pruning doing exactly that. So a walk whose
// head does not start at zero is reported (ChainVerification.Genesis == false)
// rather than treated as either proof or tampering. Everything else -- a
// modified record, a reordered, duplicated, truncated or deleted middle or
// tail archive -- breaks a link and is reported.
// ---------------------------------------------------------------------------

// maxScanLine bounds a single NDJSON record. A line longer than this is a
// corrupt or hostile file, not a record, and the scanner reports it rather
// than allocating without limit.
const maxScanLine = 1024 * 1024

// chainAnchorAction is the Action of the record rotate() appends to an
// outgoing file. See isChainAnchor for why the whole triple is matched.
const chainAnchorAction = "chain-anchor"

// chainAnchorActor and chainAnchorResource complete the anchor's identity.
const (
	chainAnchorActor    = "system"
	chainAnchorResource = "audit"
)

// ChainVerification reports what a rotation-set verification covered.
//
// It exists because "no error" is not the whole answer: a set whose oldest
// archives have been pruned verifies cleanly from a head that is not the
// chain's origin, and a caller that cannot see the difference cannot state
// what it proved.
type ChainVerification struct {
	// Files is the number of files that carry records and were linked.
	Files int

	// Records is the number of audit records verified, anchors excluded.
	// int64: a trail's record count is unbounded over its retention window.
	Records int64

	// Order lists the linked files in chain order, head first. It is derived
	// from the digests, not from the file names, so a renamed archive does not
	// change it.
	Order []string

	// Genesis reports whether the head of the walk is the origin of the chain
	// -- its first record carries no PrevHash. False means the walk began
	// mid-chain, which is what retention pruning leaves behind and is also
	// what deleting the oldest archives would leave behind. The two are not
	// distinguishable by hash.
	Genesis bool

	// Seed is the hex digest the head claims to continue from, empty at
	// genesis. Handoff is the hex digest the tail ends on.
	Seed    string
	Handoff string
}

// chainSegment is one verified file of a rotation set.
type chainSegment struct {
	path string

	// seed is the digest the file's first record declares as its predecessor.
	seed [sha256.Size]byte

	// handoff is the digest this file passes to its successor: the running
	// digest at the position of a TERMINAL chain-anchor, or the final digest
	// when the file has none (the active file). It mirrors exactly what
	// l.prevDigest held when the file stopped being the active file.
	handoff [sha256.Size]byte

	// records counts folded records excluding anchors. A file with none holds
	// no audit records and is not a link in the chain.
	records int64
}

// expectedPrevHash returns the PrevHash a record must carry for a chain
// standing at prev.
//
// The rule is Emit's, unconditionally: a zero digest is written as an EMPTY
// PrevHash, never as sixty-four zeros. The reader previously qualified that
// with "only at index 0", which the writer never did.
func expectedPrevHash(prev [sha256.Size]byte) string {
	if allZero(prev) {
		return ""
	}
	return hex.EncodeToString(prev[:])
}

// isChainAnchor reports whether ev is the record rotate() writes to close an
// outgoing file.
//
// All three fields are matched because the anchor is the only record whose
// position changes what the verifier computes, so its identity is pinned as
// narrowly as the writer states it. A forged anchor buys nothing regardless:
// it must still carry the PrevHash the fold demands, and only a TERMINAL
// anchor moves the handoff -- which only matters to a successor file, and the
// file holding a forged terminal anchor is by definition the one being
// checked against its own successor.
func isChainAnchor(ev AuditEvent) bool {
	return ev.Action == chainAnchorAction &&
		ev.Actor == chainAnchorActor &&
		ev.Resource == chainAnchorResource
}

// decodeDigest parses a record's PrevHash. An empty value is the zero digest.
func decodeDigest(prevHash string) ([sha256.Size]byte, bool) {
	var out [sha256.Size]byte
	if prevHash == "" {
		return out, true
	}
	raw, err := hex.DecodeString(prevHash)
	if err != nil || len(raw) != sha256.Size {
		return out, false
	}
	copy(out[:], raw)
	return out, true
}

// verifyChainSegment walks one file, seeded with the digest its own first
// record declares, and returns what it hands to its successor.
//
// SELF-SEEDING IS DELIBERATE AND IS NOT A WEAKENING. It makes the check on the
// first record vacuous and every other check exact: record n+1's PrevHash must
// equal the fold over record n, so any edit inside the file is caught, and a
// terminal anchor pins the last record too. What the seed is worth is settled
// by verifyChainSet, which requires it to equal the predecessor's handoff.
// Verifying against a caller-supplied seed instead would report the same file
// twice -- once as a break here and once as a broken link there -- for one
// fault.
//
// A line that will not unmarshal is SKIPPED, not folded, matching
// initPrevDigest: a torn line is not a record, and both readers must agree on
// that or they diverge on the one file a crash leaves behind.
func verifyChainSegment(ctx context.Context, path string) (*chainSegment, error) {
	file, err := os.Open(path) // #nosec G304 -- path is the audit sink's own configured file or an archive of it.
	if err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	defer func() { _ = file.Close() }()

	seg := &chainSegment{path: path}
	var prev [sha256.Size]byte
	var preAnchor [sha256.Size]byte
	seeded := false
	lastWasAnchor := false
	index := 0

	scanner := bufio.NewScanner(file)
	// Small initial buffer, large ceiling: a set walk opens every archive in
	// the directory, and pre-allocating the ceiling per file cost ~1 MiB each.
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLine)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, &VerificationCanceledError{Path: path, Err: err}
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			index++
			continue
		}

		if !seeded {
			seed, ok := decodeDigest(ev.PrevHash)
			if !ok {
				return nil, &ChainBrokenError{
					Path:     path,
					Index:    index,
					Expected: "hex-encoded sha-256 digest",
					Got:      ev.PrevHash,
				}
			}
			seg.seed = seed
			prev = seed
			seeded = true
		} else if want := expectedPrevHash(prev); ev.PrevHash != want {
			return nil, &ChainBrokenError{
				Path:     path,
				Index:    index,
				Expected: want,
				Got:      ev.PrevHash,
			}
		}

		canonical, err := canonicalJSON(ev)
		if err != nil {
			return nil, &ChainBrokenError{
				Path:     path,
				Index:    index,
				Expected: "valid json",
				Got:      "marshal error",
			}
		}

		if isChainAnchor(ev) {
			preAnchor = prev
			lastWasAnchor = true
		} else {
			seg.records++
			lastWasAnchor = false
		}
		prev = chainDigest(prev, canonical)
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}

	seg.handoff = prev
	if lastWasAnchor {
		seg.handoff = preAnchor
	}
	return seg, nil
}

// verifyChainFile verifies one file of a rotation set on its own, reporting
// *ChainBrokenError at the first record that does not follow its predecessor.
//
// It says nothing about where the file sits in the set -- that is
// verifyChainSet's question.
func verifyChainFile(ctx context.Context, path string) error {
	_, err := verifyChainSegment(ctx, path)
	return err
}

// rotationSetFiles lists the rotation set whose active log is path: the
// archives in name order, then the active file when it is there. active
// reports whether it was.
//
// A directory that cannot be read is an ERROR, never an empty set -- reporting
// "no files" would let a verification pass by seeing nothing.
//
// Whether a MISSING ACTIVE FILE is a fault is not decided here, because from
// one listing it cannot be: a rotation unlinks the active name and re-creates
// it a moment later, so its absence is either an unlinked audit log or an
// ordinary rotation caught mid-stride. verifyChainSet decides, from two
// listings.
//
// The ORDER IS NOT TRUSTED. It only decides which file a diagnostic mentions
// first; the chain order verifyChainSet reports is derived from the digests.
func rotationSetFiles(path string) (paths []string, active bool, err error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, &FileError{Path: dir, Err: err}
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == base {
			active = true
			continue
		}
		if !strings.HasPrefix(name, stem+"-") || filepath.Ext(name) != ext {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Strings(paths)

	if active {
		paths = append(paths, path)
	}
	return paths, active, nil
}

// verifyScanAttempts bounds how many times a set walk is repeated when the
// sink moved underneath it, and verifyScanBackoff is the pause before the
// first retry, doubling thereafter. See verifyChainSet.
//
// The pause exists so that "every attempt saw the same thing" means something.
// A rotation is a rename, a directory fsync and an open, all under the
// writer's lock: retrying with no delay can sample the same instant of it
// several times over. Fifteen milliseconds all told, on a path that reads the
// entire trail and has no request waiting on it.
const (
	verifyScanAttempts = 5
	verifyScanBackoff  = time.Millisecond
)

// verifyChainSet verifies every file of the rotation set whose active log is
// path, then links them by digest.
//
// Two independent properties, in this order:
//
//  1. Each file is internally consistent (verifyChainSegment).
//  2. The files form ONE chain -- exactly one head, every other file entered
//     through some predecessor's handoff, no file left over.
//
// A MISSING ACTIVE FILE IS A FAULT, not an empty set. Unlinking a live audit
// log would otherwise verify clean over the archives alone -- or, before the
// first rotation, over nothing at all -- while the sink kept writing to an
// unreachable inode.
//
// IT RUNS WITHOUT THE WRITER'S LOCK, and re-reads the directory listing to
// find out whether it got a consistent view.
//
// A rotation renames the active file and creates a new one, so a walk that
// straddles one can list the set before the rotation and open the files after
// it: the new archive is missing from the list, the replacement active file's
// seed has no predecessor in it, and the walk reports a broken link ON HONEST
// DATA -- the exact failure this whole change exists to remove, reintroduced
// in a new place.
//
// Holding l.mu across the walk would fix it and is the wrong trade: this reads
// the entire trail, and every authorization decision in the node blocks behind
// that lock while it does (go-qrdb#335). So the listing is taken, the walk
// runs, and the listing is taken again. Archive names are only ever added, so
// an unchanged listing means no rotation completed in the window and the walk
// saw one generation. A changed listing means retry.
//
// Appends do not need the check -- a walk that reads a longer active file
// still links -- and neither does the anchor: rotate() appends it before the
// rename, and a file's handoff is the same value whether the anchor has landed
// yet, fully, or not at all.
//
// Failing to get a stable view returns *ConcurrentRotationError, which says
// the trail moved, NOT that anything is wrong with it. Reporting a tamper
// verdict from a race is the one outcome that is worse than reporting nothing.
//
// Files holding no audit records -- an archive that is nothing but an anchor,
// or the empty file a rotation has just opened -- are verified and then left
// out of the linking. They hand off nothing, so treating them as links would
// fork the walk on honest data.
func verifyChainSet(ctx context.Context, path string) (*ChainVerification, error) {
	return verifyChainSetAt(ctx, path, nil)
}

// verifyChainSetAt is verifyChainSet with the writer's rotation sequence
// (ChainFileLogger.rotations), when the set's writer is in this process.
//
// The two listings cannot see a rotation that is MID-STRIDE across both of
// them: a writer stalled between its rename and its re-open -- descheduled on
// a loaded host for longer than every retry together -- leaves the same
// listing, active name absent, on both sides of every attempt, and that is
// exactly what an unlinked audit log looks like. The sequence settles it: an
// attempt counts only if it read the same EVEN value before its first listing
// and after its second, so no rotation began, finished or was in progress
// while it looked. Any other attempt is a race, never an absence. A nil
// generation -- a set whose writer is not in this process -- leaves the
// listings as the only evidence.
func verifyChainSetAt(ctx context.Context, path string, generation func() uint64) (*ChainVerification, error) {
	backoff := verifyScanBackoff
	stableAbsences := 0
	for attempt := 0; attempt < verifyScanAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, &VerificationCanceledError{Path: path, Err: err}
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, &VerificationCanceledError{Path: path, Err: ctx.Err()}
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		var genBefore uint64
		if generation != nil {
			genBefore = generation()
		}
		before, active, err := rotationSetFiles(path)
		if err != nil {
			return nil, err
		}
		v, walkErr := linkChainSet(ctx, before)
		after, _, err := rotationSetFiles(path)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(before, after) {
			continue // the sink rotated across the walk; that view is mixed
		}
		if generation != nil {
			if genAfter := generation(); genBefore != genAfter || genBefore%2 == 1 {
				continue // a rotation began, ended or was mid-stride while it looked
			}
		}
		if !active {
			// Absent across a walk that saw no rotation. That is what an
			// unlinked audit log looks like -- and also, narrowly, what a
			// rotation looks like if both listings land between its rename
			// and its re-open. Only a verdict once EVERY attempt has said so.
			stableAbsences++
			continue
		}
		return v, walkErr
	}

	if stableAbsences == verifyScanAttempts {
		return nil, &FileError{Path: path, Err: fs.ErrNotExist}
	}
	return nil, &ConcurrentRotationError{Path: path, Attempts: verifyScanAttempts}
}

// linkChainSet verifies each file and links them into one chain. It is
// verifyChainSet's body, separated so the caller can repeat it against a
// fresh listing.
func linkChainSet(ctx context.Context, paths []string) (*ChainVerification, error) {
	segs := make([]*chainSegment, 0, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, &VerificationCanceledError{Path: p, Err: err}
		}
		seg, err := verifyChainSegment(ctx, p)
		if err != nil {
			return nil, err
		}
		if seg.records == 0 {
			continue
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return &ChainVerification{}, nil
	}

	bySeed := make(map[string]*chainSegment, len(segs))
	byHandoff := make(map[string]*chainSegment, len(segs))
	for _, s := range segs {
		seed := hex.EncodeToString(s.seed[:])
		if other, dup := bySeed[seed]; dup {
			return nil, &ChainLinkError{Path: s.path, Other: other.path, Digest: seed, Reason: LinkForked}
		}
		bySeed[seed] = s

		// byHandoff answers one question -- "does anything hand off to this
		// file" -- so a duplicate key here needs no check of its own: two
		// files ending on the same digest is a SHA-256 collision, and the
		// second one would surface below as a file nothing reaches.
		byHandoff[hex.EncodeToString(s.handoff[:])] = s
	}

	// A head is a file no other file hands off to. A sound set has exactly
	// one. A set missing a file in the MIDDLE has two, and the second one --
	// the orphan -- is the diagnostic worth reporting, so the walk starts at
	// the genesis file whenever the set still has one and leaves every other
	// head to be named below for what it is: a file whose predecessor is gone.
	var heads []*chainSegment
	var genesis *chainSegment
	for _, s := range segs {
		if _, entered := byHandoff[hex.EncodeToString(s.seed[:])]; entered {
			continue
		}
		heads = append(heads, s)
		if allZero(s.seed) {
			genesis = s
		}
	}
	head := genesis
	if head == nil {
		switch len(heads) {
		case 0:
			// Every file is entered by another, which for a finite set means
			// the links form a cycle -- which needs a SHA-256 collision, so
			// this is a guard rather than a reachable state. It is a typed
			// error and not a panic or a silent heads[0], because the whole
			// point of this walk is that it never claims more than it knows.
			return nil, &ChainLinkError{Path: segs[0].path, Reason: LinkNoHead}
		case 1:
			// The origin has been pruned away. Reported, not refused; see
			// ChainVerification.Genesis.
			head = heads[0]
		default:
			// Several files begin nothing and none is the origin, so nothing
			// says which one retention left behind.
			return nil, &ChainLinkError{
				Path:   heads[0].path,
				Other:  heads[1].path,
				Reason: LinkMultipleHeads,
			}
		}
	}

	result := &ChainVerification{
		Order:   make([]string, 0, len(segs)),
		Genesis: allZero(head.seed),
		Seed:    expectedPrevHash(head.seed),
	}
	visited := make(map[*chainSegment]bool, len(segs))
	for cur := head; cur != nil && !visited[cur]; cur = bySeed[hex.EncodeToString(cur.handoff[:])] {
		visited[cur] = true
		result.Order = append(result.Order, cur.path)
		result.Records += cur.records
		result.Files++
		result.Handoff = hex.EncodeToString(cur.handoff[:])
	}

	for _, s := range segs {
		if !visited[s] {
			return nil, &ChainLinkError{
				Path:   s.path,
				Digest: hex.EncodeToString(s.seed[:]),
				Reason: LinkNoPredecessor,
			}
		}
	}
	return result, nil
}
