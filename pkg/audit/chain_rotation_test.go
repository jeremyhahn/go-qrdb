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

// Rotation-boundary verification (go-qrdb#452).
//
// No build constraint, deliberately: the chain is the tamper-evidence property
// of a sink every authorization decision now goes through (go-qrdb#335), and
// these tests must compile in every build.
//
// EVERY TEST HERE DRIVES THE REAL ChainFileLogger THROUGH A REAL ROTATION.
// rotateFunc is never installed: it replaces rotate() wholesale, so a double
// would remove the anchor write, the rename and the reopen -- which is to say
// it would remove the entire subject.

package audit

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// rotatingLogger returns a real logger whose maxBytes forces a rotation every
// few records, and the directory it writes into.
func rotatingLogger(t *testing.T, maxBytes int64) (*ChainFileLogger, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(maxBytes))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	return l, dir, path
}

// emitN emits n events through the real Emit path, failing on any refusal.
func emitN(t *testing.T, l *ChainFileLogger, n int, action string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := l.Emit(ctx, testEvent("alice", "barrier", action)); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
}

// setFiles lists the rotation set in name order.
func setFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, filepath.Join(dir, e.Name()))
	}
	sort.Strings(names)
	return names
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a file this test just created under t.TempDir().
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	// #nosec G306 -- the audit file's own mode; this rewrites one in a temp dir.
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Honest data must verify
// ---------------------------------------------------------------------------

// TestChainFileLogger_VerifyChain_AcrossRotations_EveryFileVerifies is the
// go-qrdb#452 regression, stated the way the issue asked for it: emit across
// several rotation boundaries and verify EVERY resulting file, not merely
// count them.
//
// Before the fix, every file opened after a rotation failed at index 0 --
// "expected prevHash <empty>, got <64 hex>" -- because the writer carried the
// chain across the boundary and the reader assumed each file restarted at
// zero. Five of six files in the original six-event reproduction.
func TestChainFileLogger_VerifyChain_AcrossRotations_EveryFileVerifies(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)
	emitN(t, l, 12, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	files := setFiles(t, dir)
	if len(files) < 4 {
		t.Fatalf("the test needs several rotation boundaries; got %d files: %v", len(files), files)
	}
	for _, f := range files {
		if err := verifyChainFile(context.Background(), testTrailDir(t, f), f); err != nil {
			t.Errorf("%s: verify failed on honest data: %v", filepath.Base(f), err)
		}
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet on honest data: %v", err)
	}
	if !v.Genesis {
		t.Error("the set was never pruned, so the walk must reach the chain's origin")
	}
	if v.Seed != "" {
		t.Errorf("Seed = %q; the origin continues from nothing, and the writer spells "+
			"that as an EMPTY PrevHash rather than sixty-four zeros", v.Seed)
	}
	if v.Handoff == "" {
		t.Error("a trail holding records must end on a digest")
	}
	if v.Records != 12 {
		t.Errorf("Records = %d, want the 12 emitted; anchors are not records", v.Records)
	}
	if v.Files != len(files) {
		t.Errorf("linked %d files, want all %d", v.Files, len(files))
	}
}

// TestChainFileLogger_VerifyChain_ViaMethodAcrossRotations drives the exported
// method rather than the package helper, because the method is what an auditor
// calls and it is the method's contract that changed to cover the whole set.
func TestChainFileLogger_VerifyChain_ViaMethodAcrossRotations(t *testing.T) {
	l, _, _ := rotatingLogger(t, 400)
	defer func() { _ = l.Close() }()
	emitN(t, l, 10, "seal")

	if err := l.VerifyChain(context.Background()); err != nil {
		t.Fatalf("VerifyChain across rotations: %v", err)
	}
}

// TestChainFileLogger_VerifyChain_SurvivesARestartAcrossARotation.
//
// The second face of #452, and it is not fixed by the reader alone.
// initPrevDigest folded the active file from ZERO, but a file opened after a
// rotation begins mid-chain, so a restart produced a digest no record in the
// file agrees with and the next event written linked to it -- measured on the
// pre-fix tree as a break at index 1 of the active file. The seed now comes
// from the first record's stored PrevHash.
func TestChainFileLogger_VerifyChain_SurvivesARestartAcrossARotation(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)
	emitN(t, l, 8, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(setFiles(t, dir)) < 2 {
		t.Fatal("the test needs at least one rotation before the restart")
	}

	// Re-open the way a daemon restart does, and keep writing.
	l2, err := NewFileLogger(path, WithMaxBytes(1<<30))
	if err != nil {
		t.Fatalf("NewFileLogger (restart): %v", err)
	}
	emitN(t, l2, 4, "rekey")
	if err := l2.Close(); err != nil {
		t.Fatalf("Close (restart): %v", err)
	}

	if err := verifyChainFile(context.Background(), testTrailDir(t, path), path); err != nil {
		t.Fatalf("the active file must verify after a restart across a rotation: %v", err)
	}
	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet after restart: %v", err)
	}
	if v.Records != 12 {
		t.Errorf("Records = %d, want 12 (8 before the restart, 4 after)", v.Records)
	}
}

// TestChainFileLogger_VerifyChainSet_ChainOrderComesFromDigestsNotNames.
//
// The walk is built from the handoff digests, so renaming the archives -- the
// cheapest way to disguise a reordering -- changes nothing about the order it
// reports. Asserting this is what stops the order quietly degrading into a
// filename sort, which the >999-rotations-a-day fallback name would break.
func TestChainFileLogger_VerifyChainSet_ChainOrderComesFromDigestsNotNames(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)
	emitN(t, l, 10, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}

	// Reverse the archives' name ordering while keeping them in the set.
	archives := before.Order[:len(before.Order)-1]
	renamed := make([]string, len(archives))
	for i, p := range archives {
		renamed[i] = filepath.Join(dir, "audit-2099-01-01-"+itoa(len(archives)-i)+".ndjson")
		if err := os.Rename(p, renamed[i]); err != nil {
			t.Fatalf("rename: %v", err)
		}
	}

	after, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet after renaming the archives: %v", err)
	}
	if after.Files != before.Files || after.Records != before.Records {
		t.Fatalf("renaming changed the walk: %d/%d files, %d/%d records",
			after.Files, before.Files, after.Records, before.Records)
	}
	for i, p := range after.Order[:len(renamed)] {
		if p != renamed[i] {
			t.Errorf("chain position %d = %s, want %s; the order must follow the digests",
				i, filepath.Base(p), filepath.Base(renamed[i]))
		}
	}
}

// TestChainFileLogger_VerifyChainSet_APrunedHeadIsNotTampering.
//
// Retention deletes the oldest archives, so a healthy trail's walk routinely
// begins mid-chain. Reporting that as a break would reinstate exactly the
// cry-wolf behaviour #452 is about. It is reported as Genesis == false, which
// is the honest answer: the digests were not traced back to zero, and no hash
// walk can tell retention from someone deleting the same files.
func TestChainFileLogger_VerifyChainSet_APrunedHeadIsNotTampering(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	emitN(t, l, 12, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	full, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	if err := os.Remove(full.Order[0]); err != nil {
		t.Fatalf("prune the oldest archive: %v", err)
	}

	pruned, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("a pruned head must not be reported as a broken chain: %v", err)
	}
	if pruned.Genesis {
		t.Error("Genesis must be false once the origin has been pruned away")
	}
	if pruned.Seed == "" {
		t.Error("a mid-chain head must report the digest it claims to continue from")
	}
	if pruned.Files != full.Files-1 {
		t.Errorf("Files = %d, want %d", pruned.Files, full.Files-1)
	}
}

// TestChainFileLogger_VerifyChainSet_EmptyAndRecordlessFilesAreNotLinks.
//
// A rotation opens an empty active file; maxBytes below one line's length can
// leave an archive holding nothing but its anchor. Neither hands anything off,
// so treating them as links would fork the walk on honest data.
func TestChainFileLogger_VerifyChainSet_EmptyAndRecordlessFilesAreNotLinks(t *testing.T) {
	l, dir, path := rotatingLogger(t, 1)
	emitN(t, l, 4, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// maxBytes of 1 rotates on the very first event, before the file holds a
	// record, so the first archive is an anchor and nothing else.
	files := setFiles(t, dir)
	recordless := 0
	for _, f := range files {
		seg, err := verifyChainSegment(context.Background(), testTrailDir(t, f), f)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		if seg.records == 0 {
			recordless++
		}
	}
	if recordless == 0 {
		t.Fatal("expected at least one record-less file; the case under test did not arise")
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("record-less files must not fork the walk: %v", err)
	}
	if v.Records != 4 {
		t.Errorf("Records = %d, want the 4 emitted", v.Records)
	}
}

// TestVerifyChainSet_EmptyDirectoryIsNotAFailure: a logger that has never
// written has nothing to contradict.
func TestVerifyChainSet_EmptyDirectoryIsNotAFailure(t *testing.T) {
	l, _, path := rotatingLogger(t, 1<<20)
	defer func() { _ = l.Close() }()

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet on an empty trail: %v", err)
	}
	if v.Files != 0 || v.Records != 0 || v.Genesis {
		t.Errorf("empty trail reported %+v; want a zero verification", v)
	}
}

// ---------------------------------------------------------------------------
// Tampering must be reported
// ---------------------------------------------------------------------------

// TestChainFileLogger_VerifyChain_CatchesARecordEditedInAnArchive is the other
// direction of the #452 requirement: a real modification of a real archive,
// caught, and named against the file it happened in.
func TestChainFileLogger_VerifyChain_CatchesARecordEditedInAnArchive(t *testing.T) {
	l, _, path := rotatingLogger(t, 900)
	emitN(t, l, 16, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet before tampering: %v", err)
	}
	if len(v.Order) < 3 {
		t.Fatalf("need a middle archive; got %d files", len(v.Order))
	}
	victim := v.Order[1]

	lines := readLines(t, victim)
	if len(lines) < 2 {
		t.Fatalf("%s holds %d lines; need a record before the anchor", victim, len(lines))
	}
	var ev AuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ev.Outcome = OutcomeSuccess + "-but-actually-denied"
	edited, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines[0] = string(edited)
	writeLines(t, victim, lines)

	var broken *ChainBrokenError
	err = verifyChainSetErr(t, path)
	if !errors.As(err, &broken) {
		t.Fatalf("editing a record in an archive must be reported as *ChainBrokenError; got %v", err)
	}
	if broken.Path != victim {
		t.Errorf("the break was reported against %s, want %s", broken.Path, victim)
	}
}

// TestChainFileLogger_VerifyChain_CatchesAnEditToAnArchivesFirstRecord.
//
// The check on a file's FIRST record is vacuous by construction -- the seed is
// taken from it. That is safe only because the record after it, or the anchor
// that terminates the file, pins it. This is the test that says so, because it
// is the property a reader is entitled to doubt.
func TestChainFileLogger_VerifyChain_CatchesAnEditToAnArchivesFirstRecord(t *testing.T) {
	l, _, path := rotatingLogger(t, 900)
	emitN(t, l, 16, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	victim := v.Order[1]
	lines := readLines(t, victim)

	var ev AuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ev.Actor = "mallory"
	edited, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines[0] = string(edited)
	writeLines(t, victim, lines)

	if err := verifyChainFile(context.Background(), testTrailDir(t, victim), victim); err == nil {
		t.Fatal("an edit to the first record of an archive must still be caught, " +
			"by the record that follows it or by the anchor that terminates the file")
	}
}

// TestChainFileLogger_VerifyChain_CatchesADeletedArchive is the property the
// spanning chain was chosen FOR. Per-file chains would let a whole segment be
// removed and leave every remaining file verifying perfectly.
func TestChainFileLogger_VerifyChain_CatchesADeletedArchive(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	emitN(t, l, 12, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	if len(v.Order) < 4 {
		t.Fatalf("need a middle archive to delete; got %d files", len(v.Order))
	}
	victim := v.Order[1]
	if err := os.Remove(victim); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Every surviving file is still internally sound -- which is exactly why a
	// per-file verifier would have reported nothing.
	for _, f := range v.Order {
		if f == victim {
			continue
		}
		if err := verifyChainFile(context.Background(), testTrailDir(t, f), f); err != nil {
			t.Fatalf("%s stopped verifying on its own: %v", filepath.Base(f), err)
		}
	}

	var link *ChainLinkError
	err = verifyChainSetErr(t, path)
	if !errors.As(err, &link) {
		t.Fatalf("deleting an archive must be reported as *ChainLinkError; got %v", err)
	}
	if link.Reason != LinkNoPredecessor {
		t.Errorf("Reason = %q, want %q", link.Reason, LinkNoPredecessor)
	}
	if link.Path != v.Order[2] {
		t.Errorf("the orphan was reported as %s, want %s",
			filepath.Base(link.Path), filepath.Base(v.Order[2]))
	}
}

// TestChainFileLogger_VerifyChain_CatchesATruncatedArchive: dropping the last
// record before the anchor. The anchor pins it, which is the second job the
// anchor does once the chain spans.
func TestChainFileLogger_VerifyChain_CatchesATruncatedArchive(t *testing.T) {
	l, _, path := rotatingLogger(t, 900)
	emitN(t, l, 16, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	victim := v.Order[1]
	lines := readLines(t, victim)
	if len(lines) < 3 {
		t.Fatalf("%s holds %d lines; need at least two records and an anchor", victim, len(lines))
	}
	// Remove the record immediately before the terminating anchor.
	writeLines(t, victim, append(lines[:len(lines)-2], lines[len(lines)-1]))

	var broken *ChainBrokenError
	if err := verifyChainFile(context.Background(), testTrailDir(t, victim), victim); !errors.As(err, &broken) {
		t.Fatalf("a truncated archive must be reported as *ChainBrokenError; got %v", err)
	}
}

// TestChainFileLogger_VerifyChain_CatchesADuplicatedArchive: a trail is a
// line, not a tree. Replaying an archive under a second name puts two files in
// the same chain position.
func TestChainFileLogger_VerifyChain_CatchesADuplicatedArchive(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)
	emitN(t, l, 12, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	data, err := os.ReadFile(v.Order[1]) // #nosec G304 -- a file this test just created under t.TempDir().
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// #nosec G306,G703 -- mirrors the audit file's own mode; the path is a literal name under this test's t.TempDir().
	if err := os.WriteFile(filepath.Join(dir, "audit-2099-12-31-000.ndjson"), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var link *ChainLinkError
	err = verifyChainSetErr(t, path)
	if !errors.As(err, &link) {
		t.Fatalf("a duplicated archive must be reported as *ChainLinkError; got %v", err)
	}
	if link.Reason != LinkForked {
		t.Errorf("Reason = %q, want %q", link.Reason, LinkForked)
	}
}

// TestChainFileLogger_VerifyChain_CatchesAReseatedArchive: rewriting only an
// archive's declared seed, so the file verifies internally against the new
// seed but no longer follows its predecessor. This is the fault self-seeding
// trades away at the file level and the set walk exists to catch.
func TestChainFileLogger_VerifyChain_CatchesAReseatedArchive(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	emitN(t, l, 12, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	victim := v.Order[2]
	lines := readLines(t, victim)

	var ev AuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var forged [32]byte
	forged[0] = 0xAA
	ev.PrevHash = hex.EncodeToString(forged[:])
	reseated, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines[0] = string(reseated)
	writeLines(t, victim, lines)

	// Re-seating breaks the file internally too, because the anchor still
	// pins the old fold -- but if it did not, the walk would catch it.
	if err := verifyChainFile(context.Background(), testTrailDir(t, victim), victim); err == nil {
		if err := verifyChainSetErr(t, path); err == nil {
			t.Fatal("re-seating an archive must be reported by one verifier or the other")
		}
	}
}

// TestChainFileLogger_VerifyChain_TamperInTheActiveFileAcrossRotations keeps
// the original single-file tamper property alive now that VerifyChain covers
// the whole set: a doctored record in the live file is still caught.
func TestChainFileLogger_VerifyChain_TamperInTheActiveFileAcrossRotations(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	emitN(t, l, 8, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// More records into the active file, so the doctored one is not the tip.
	// A chain cannot pin its own last record until another follows it.
	l1, err := NewFileLogger(path, WithMaxBytes(1<<30))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	emitN(t, l1, 3, "rekey")
	if err := l1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) < 2 {
		t.Fatalf("the active file holds %d lines; need at least two", len(lines))
	}
	var ev AuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ev.Action = "tampered"
	edited, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines[0] = string(edited)
	writeLines(t, path, lines)

	l2, err := NewFileLogger(path, WithMaxBytes(1<<30))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l2.Close() }()

	if err := l2.VerifyChain(context.Background()); err == nil {
		t.Fatal("a doctored record in the active file must still be reported")
	}
}

// ---------------------------------------------------------------------------
// Helpers under test in their own right
// ---------------------------------------------------------------------------

func TestExpectedPrevHash_ZeroIsEmpty(t *testing.T) {
	var zero [32]byte
	if got := expectedPrevHash(zero); got != "" {
		t.Fatalf("expectedPrevHash(zero) = %q, want empty -- Emit writes an empty "+
			"PrevHash for a zero digest and the reader must use the same rule", got)
	}
}

func TestExpectedPrevHash_NonZeroIsHex(t *testing.T) {
	var d [32]byte
	d[31] = 1
	want := hex.EncodeToString(d[:])
	if got := expectedPrevHash(d); got != want {
		t.Fatalf("expectedPrevHash = %q, want %q", got, want)
	}
}

func TestIsChainAnchor_MatchesTheRecordRotateWrites(t *testing.T) {
	ev := AuditEvent{Actor: chainAnchorActor, Resource: chainAnchorResource, Action: chainAnchorAction}
	if !isChainAnchor(ev) {
		t.Fatal("the record rotate() writes must be recognised as an anchor")
	}
}

func TestIsChainAnchor_RejectsANearMiss(t *testing.T) {
	cases := map[string]AuditEvent{
		"actor": {Actor: "alice", Resource: chainAnchorResource, Action: chainAnchorAction},
		"resource": {Actor: chainAnchorActor, Resource: "barrier",
			Action: chainAnchorAction},
		"action": {Actor: chainAnchorActor, Resource: chainAnchorResource, Action: "unseal"},
	}
	for name, ev := range cases {
		if isChainAnchor(ev) {
			t.Errorf("%s differs from the anchor and must not be treated as one", name)
		}
	}
}

func TestDecodeDigest_EmptyIsZero(t *testing.T) {
	d, ok := decodeDigest("")
	if !ok || !allZero(d) {
		t.Fatalf("decodeDigest(%q) = %x,%v; want the zero digest", "", d, ok)
	}
}

func TestDecodeDigest_RejectsMalformed(t *testing.T) {
	for _, s := range []string{"zz", "abcd", strings.Repeat("a", 63), strings.Repeat("a", 66)} {
		if _, ok := decodeDigest(s); ok {
			t.Errorf("decodeDigest(%q) accepted a value that is not a sha-256 digest", s)
		}
	}
}

func TestDecodeDigest_RoundTrips(t *testing.T) {
	var d [32]byte
	for i := range d {
		d[i] = byte(i)
	}
	got, ok := decodeDigest(hex.EncodeToString(d[:]))
	if !ok || got != d {
		t.Fatalf("decodeDigest round trip = %x,%v", got, ok)
	}
}

func TestRotationSetFiles_ArchivesThenActive(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)
	emitN(t, l, 8, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A neighbour that is not part of the set.
	// #nosec G306 -- a decoy file in a temp dir.
	if err := os.WriteFile(filepath.Join(dir, "audit.ndjson.bak"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	files, _, err := rotationSetFiles(testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("rotationSetFiles: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("got %d files, want the archives and the active file", len(files))
	}
	if files[len(files)-1] != path {
		t.Errorf("the active file must come last; got %s", files[len(files)-1])
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".bak") {
			t.Errorf("%s is not part of the rotation set", f)
		}
	}
}

// TestRotationSetFiles_AMissingActiveFileIsRefused.
//
// A verifier that cannot see the file it was asked about must not report
// success. Unlinking a live audit log would otherwise verify clean over the
// archives alone -- or, before the first rotation, over nothing at all --
// while the sink went on writing to an unreachable inode.
func TestRotationSetFiles_AMissingActiveFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(400))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	emitN(t, l, 6, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the active file: %v", err)
	}

	_, active, err := rotationSetFiles(testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("listing the set must still work: %v", err)
	}
	if active {
		t.Fatal("the active file was removed; the listing must say so")
	}

	var fErr *FileError
	verifyErr := verifyChainSetErr(t, path)
	if !errors.As(verifyErr, &fErr) {
		t.Fatalf("verifying a trail whose active file was deleted must fail; got %v", verifyErr)
	}
	if !errors.Is(verifyErr, fs.ErrNotExist) {
		t.Errorf("the error must say the file is not there; got %v", verifyErr)
	}
	if fErr.Path != path {
		t.Errorf("the error names %s, want the active file %s", fErr.Path, path)
	}
}

func TestRotationSetFiles_MissingDirectoryIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone", "audit.ndjson")
	// The trail's directory holds the missing one.
	trail := testTrailDir(t, filepath.Dir(path))
	var fErr *FileError
	if _, _, err := rotationSetFiles(trail, path); !errors.As(err, &fErr) {
		t.Fatalf("a missing directory must be a *FileError, not an empty set; got %v", err)
	}
	if _, err := verifyChainSet(context.Background(), trail, path); !errors.As(err, &fErr) {
		t.Fatalf("verifyChainSet must propagate it; got %v", err)
	}
}

func TestVerifyChainSegment_RejectsAMalformedSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	writeLines(t, path, []string{`{"timestamp":"2026-01-01T00:00:00Z","actor":"a","resource":"r","action":"x","outcome":"success","prev_hash":"not-hex"}`})

	var broken *ChainBrokenError
	if _, err := verifyChainSegment(context.Background(), testTrailDir(t, path), path); !errors.As(err, &broken) {
		t.Fatalf("a seed that is not a digest must be a *ChainBrokenError; got %v", err)
	}
}

func TestVerifyChainSegment_EmptyFileHasNoRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	// #nosec G306,G703 -- mirrors the audit file's own mode; the path is a literal name under this test's t.TempDir().
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	seg, err := verifyChainSegment(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSegment on an empty file: %v", err)
	}
	if seg.records != 0 || !allZero(seg.handoff) {
		t.Fatalf("empty file reported %d records, handoff %x", seg.records, seg.handoff)
	}
}

func TestInitPrevDigest_RefusesAMalformedSeed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	writeLines(t, path, []string{`{"timestamp":"2026-01-01T00:00:00Z","actor":"a","resource":"r","action":"x","outcome":"success","prev_hash":"not-hex"}`})

	// NewFileLogger logs and continues on a digest it cannot restore, so the
	// method is driven directly to see the error it produces.
	l := &ChainFileLogger{path: path}
	f, err := os.Open(path) // #nosec G304 -- a file this test just created under t.TempDir().
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	var broken *ChainBrokenError
	if err := l.initPrevDigest(f); !errors.As(err, &broken) {
		t.Fatalf("a head whose PrevHash is not a digest must be refused, not folded "+
			"from zero; got %v", err)
	}
	if !allZero(l.prevDigest) {
		t.Error("a refused restore must leave prevDigest untouched")
	}
}

func TestChainLinkError_MessageNamesTheFileAndTheReason(t *testing.T) {
	err := &ChainLinkError{Path: "/v/audit-1.ndjson", Other: "/v/audit-2.ndjson",
		Digest: "abc", Reason: LinkForked}
	msg := err.Error()
	for _, want := range []string{"audit-1.ndjson", "audit-2.ndjson", string(LinkForked), "abc"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
}

func TestChainLinkError_MessageWithoutOptionalFields(t *testing.T) {
	err := &ChainLinkError{Path: "/v/audit.ndjson", Reason: LinkNoHead}
	msg := err.Error()
	if strings.Contains(msg, "conflicts with") || strings.Contains(msg, "digest") {
		t.Errorf("message %q mentions fields that were not set", msg)
	}
	if !strings.Contains(msg, string(LinkNoHead)) {
		t.Errorf("message %q omits the reason", msg)
	}
}

func TestChainBrokenError_MessageNamesTheFileWhenItHasOne(t *testing.T) {
	with := (&ChainBrokenError{Path: "/v/audit.ndjson", Index: 2, Expected: "a", Got: "b"}).Error()
	if !strings.Contains(with, "/v/audit.ndjson") {
		t.Errorf("message %q omits the file", with)
	}
	without := (&ChainBrokenError{Index: 2, Expected: "a", Got: "b"}).Error()
	if strings.Contains(without, " of ") {
		t.Errorf("message %q names a file that was not set", without)
	}
}

// ---------------------------------------------------------------------------
// Small helpers keeping the tamper tests readable
// ---------------------------------------------------------------------------

// verifyChainSetErr is verifyChainSet when only the verdict is wanted.
func verifyChainSetErr(t *testing.T, path string) error {
	t.Helper()
	_, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	return err
}

// TestChainFileLogger_VerifyChainSet_CatchesTwoChainsInOneDirectory.
//
// Deleting the origin AND a file in the middle leaves two runs of archives,
// neither reaching zero. Nothing says which of them retention left behind, so
// the set is refused rather than half-verified from a guess.
func TestChainFileLogger_VerifyChainSet_CatchesTwoChainsInOneDirectory(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	emitN(t, l, 14, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v, err := verifyChainSet(context.Background(), testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
	if len(v.Order) < 5 {
		t.Fatalf("need five files to leave two runs; got %d", len(v.Order))
	}
	for _, victim := range []string{v.Order[0], v.Order[2]} {
		if err := os.Remove(victim); err != nil {
			t.Fatalf("remove %s: %v", victim, err)
		}
	}

	var link *ChainLinkError
	err = verifyChainSetErr(t, path)
	if !errors.As(err, &link) {
		t.Fatalf("two disjoint runs must be reported as *ChainLinkError; got %v", err)
	}
	if link.Reason != LinkMultipleHeads {
		t.Errorf("Reason = %q, want %q", link.Reason, LinkMultipleHeads)
	}
	if link.Other == "" {
		t.Error("the report must name both heads")
	}
}

// TestRotationSetFiles_SkipsDirectoriesAndReportsAnUnreadableParent.
//
// A directory whose name matches the archive pattern is not an archive, and a
// parent that cannot be read is an error rather than an empty set -- reporting
// "no files" there would make a verification pass by seeing nothing.
func TestRotationSetFiles_SkipsDirectoriesAndReportsAnUnreadableParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(400))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	emitN(t, l, 6, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "audit-2099-01-01-000.ndjson"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	files, _, err := rotationSetFiles(testTrailDir(t, path), path)
	if err != nil {
		t.Fatalf("rotationSetFiles: %v", err)
	}
	for _, f := range files {
		if strings.Contains(f, "2099") {
			t.Errorf("%s is a directory, not an archive", f)
		}
	}
	if err := verifyChainSetErr(t, path); err != nil {
		t.Fatalf("a directory in the set must not break verification: %v", err)
	}

	// A parent that is a regular file: ReadDir fails with something other
	// than "does not exist", and that must not read as an empty set.
	notADir := filepath.Join(dir, "audit.ndjson", "audit.ndjson")
	trail := testTrailDir(t, filepath.Dir(notADir))
	var fErr *FileError
	if _, _, err := rotationSetFiles(trail, notADir); !errors.As(err, &fErr) {
		t.Fatalf("an unreadable parent must be a *FileError; got %v", err)
	}
	if _, err := verifyChainSet(context.Background(), trail, notADir); !errors.As(err, &fErr) {
		t.Fatalf("verifyChainSet must propagate it; got %v", err)
	}
}

// TestVerifyChainSegment_ReportsALineTooLongToBeARecord: the scanner's ceiling
// is a refusal, not a silent truncation. A verifier that stopped early on an
// oversized line would report the records before it as the whole file.
func TestVerifyChainSegment_ReportsALineTooLongToBeARecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	// #nosec G306,G703 -- mirrors the audit file's own mode; the path is a literal name under this test's t.TempDir().
	if err := os.WriteFile(path, append([]byte(strings.Repeat("x", maxScanLine+1)), '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var fErr *FileError
	if _, err := verifyChainSegment(context.Background(), testTrailDir(t, path), path); !errors.As(err, &fErr) {
		t.Fatalf("an unreadable line must be a *FileError; got %v", err)
	}

	f, err := os.Open(path) // #nosec G304 -- a file this test just created under t.TempDir().
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	l := &ChainFileLogger{path: path}
	if err := l.initPrevDigest(f); !errors.As(err, &fErr) {
		t.Fatalf("initPrevDigest must report it the same way; got %v", err)
	}
}

// TestChainFileLogger_VerifyChain_NeverCriesTamperWhileTheSinkRotates.
//
// The set walk runs WITHOUT the writer's lock, so it can list the directory
// before a rotation and open the files after it. Without the generation check
// that produces a confident *ChainLinkError on a perfectly healthy trail --
// the same cry-wolf failure this change exists to remove, in a new place.
//
// A rotation is not reproducible on demand, so this drives a real sink hard
// enough to rotate many times while verifying in a tight loop, and asserts
// the ONE thing that must never happen: no run may claim the trail is broken.
// *ConcurrentRotationError is an accepted outcome -- it says the trail moved,
// not that anything is wrong with it.
//
// It verifies through the LOGGER (VerifyChainSet), the production path, which
// knows from the writer's rotation sequence whether a rotation was mid-stride
// while it listed. It used to call the path-only verifyChainSet, which cannot
// know that: under host load a writer stalled between its rename and its
// re-open for longer than every retry made each attempt see the active name
// absent, and the run failed with "file does not exist" on an honest trail.
// TestChainFileLogger_VerifyChainSet_NeverReportsTheRotationGapAsAMissingLog
// holds that window open deterministically.
func TestChainFileLogger_VerifyChain_NeverCriesTamperWhileTheSinkRotates(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)

	ctx := context.Background()
	done := make(chan struct{})
	var writeErr error
	go func() {
		defer close(done)
		for i := 0; i < 400; i++ {
			if err := l.Emit(ctx, testEvent("alice", "barrier", "unseal")); err != nil {
				writeErr = err
				return
			}
		}
	}()

	var verifications, raced int
	for {
		select {
		case <-done:
			if writeErr != nil {
				t.Fatalf("emit: %v", writeErr)
			}
			if err := l.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if verifications == 0 {
				t.Fatal("no verification ran alongside the writer")
			}
			if n := len(setFiles(t, dir)); n < 10 {
				t.Fatalf("only %d files: the sink did not rotate enough for the "+
					"window under test to arise", n)
			}
			t.Logf("%d verifications alongside the writer, %d of them raced a rotation",
				verifications, raced)
			// A final, quiescent verification must be clean.
			if err := verifyChainSetErr(t, path); err != nil {
				t.Fatalf("the finished trail must verify: %v", err)
			}
			return
		default:
		}

		verifications++
		_, err := l.VerifyChainSet(ctx)
		if err == nil {
			continue
		}
		var raceErr *ConcurrentRotationError
		if errors.As(err, &raceErr) {
			raced++
			continue
		}
		t.Fatalf("verification %d reported a fault in an honest trail: %v", verifications, err)
	}
}

func TestConcurrentRotationError_MessageIsNotATamperVerdict(t *testing.T) {
	msg := (&ConcurrentRotationError{Path: "/v/audit.ndjson", Attempts: 5}).Error()
	for _, want := range []string{"/v/audit.ndjson", "5", "Nothing is known to be wrong"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
	for _, forbidden := range []string{"broken", "tamper"} {
		if strings.Contains(strings.ToLower(msg), forbidden) {
			t.Errorf("message %q reads as a finding; it is the absence of one", msg)
		}
	}
}

// TestChainFileLogger_VerifyChain_SurvivesAFailedRename.
//
// rotate() appends the chain anchor BEFORE renaming, so a failed rename leaves
// the anchor durable in the very file the next append extends -- and openFile
// re-folds that file including the anchor. The record Emit had already
// marshalled still carried the pre-anchor digest, so appending it put a record
// after the anchor claiming the digest from before it: a break at that record,
// on entirely honest data, from a fault the sink is designed to survive and
// keep logging through.
//
// The directory fsync is failed at its boundary because a rename cannot be
// made to fail portably, and the post-state is identical either way.
func TestChainFileLogger_VerifyChain_SurvivesAFailedRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(400))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	emitN(t, l, 2, "unseal")
	filesBefore := len(setFiles(t, dir))

	// Every rotation from here on fails to commit its rename, so the anchor
	// stays in the active file and the sink keeps appending to it. The seam is
	// renameFunc, NOT rotateFunc: rotate()'s own anchor write, close, reopen
	// and failure accounting have to run, because they are the subject.
	l.mu.Lock()
	l.renameFunc = func(string, string) error { return errors.New("rename: permission denied") }
	l.mu.Unlock()
	for i := 0; i < 6; i++ {
		// The backoff bounds retries to one per minute, so drive the rotation
		// the way a long-running node would reach it.
		l.mu.Lock()
		l.rotateRetryAt = time.Time{}
		l.mu.Unlock()
		if err := l.Emit(context.Background(), testEvent("alice", "barrier", "rekey")); err != nil {
			t.Fatalf("a failed rotation is not an unwritten record: %v", err)
		}
	}
	l.mu.Lock()
	l.renameFunc = nil
	l.mu.Unlock()

	if n := len(setFiles(t, dir)); n != filesBefore {
		t.Fatalf("%d files, was %d: every rename was supposed to fail, so no archive "+
			"can have appeared", n, filesBefore)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a file this test just created under t.TempDir().
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if anchors := strings.Count(string(data), `"action":"chain-anchor"`); anchors < 2 {
		t.Fatalf("%d anchors in the file; the case under test is a buried one", anchors)
	}

	if err := verifyChainFile(context.Background(), testTrailDir(t, path), path); err != nil {
		t.Fatalf("a file carrying anchors from failed rotations is honest data and "+
			"must verify: %v", err)
	}
	if err := verifyChainSetErr(t, path); err != nil {
		t.Fatalf("verifyChainSet: %v", err)
	}
}

// TestChainFileLogger_Rotation_TheNewFileContinuesTheOutgoingAnchor pins the
// spanning contract at the boundary itself: the first record written after a
// successful rotation carries the very prevHash the outgoing file's anchor
// recorded. Both sides of the boundary state one value, which is what makes
// the cross-file link checkable at all.
func TestChainFileLogger_Rotation_TheNewFileContinuesTheOutgoingAnchor(t *testing.T) {
	l, dir, path := rotatingLogger(t, 400)
	emitN(t, l, 6, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	files := setFiles(t, dir)
	if len(files) < 2 {
		t.Fatalf("need a rotation; got %d files", len(files))
	}
	archive := files[len(files)-2] // the newest archive, active file sorts last

	archiveLines := readLines(t, archive)
	var anchor AuditEvent
	if err := json.Unmarshal([]byte(archiveLines[len(archiveLines)-1]), &anchor); err != nil {
		t.Fatalf("unmarshal anchor: %v", err)
	}
	if !isChainAnchor(anchor) {
		t.Fatalf("%s does not end with an anchor", filepath.Base(archive))
	}

	var first AuditEvent
	if err := json.Unmarshal([]byte(readLines(t, path)[0]), &first); err != nil {
		t.Fatalf("unmarshal first record of the active file: %v", err)
	}
	if first.PrevHash != anchor.PrevHash {
		t.Fatalf("the new file starts at %.16s but the outgoing anchor handed off "+
			"%.16s; the chain does not span the boundary", first.PrevHash, anchor.PrevHash)
	}
	if first.PrevHash == "" {
		t.Fatal("the handoff must be a real digest, not an empty restart")
	}
}
