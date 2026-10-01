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

package protocol

import (
	"bytes"
	"encoding/binary"
)

// ---------------------------------------------------------------------------
// Guarded commands
//
// A guarded command is any ordinary write command carried together with a
// list of compares that must all hold, at the entry's own log position, for
// the write to apply. It exists so that a precondition and the write it
// protects are ONE Raft entry: a proposer that checked the precondition and
// then proposed a plain write would leave a window between the two in which
// the precondition could stop holding, and nothing at apply time would
// notice.
//
// The motivating precondition is the database drop fence: every write to a
// namespaced database carries "this database's fence key holds the live
// incarnation the write was resolved against" (for a database whose live
// state is marker-absent, "the fence key is absent"), and DROP DATABASE
// overwrites that key with a tombstone in the same Raft group before it
// reclaims the keyspace. A write proposed before the fence but applied after it is
// refused by the state machine, on every replica, from the same log -- see
// docs/databases/engineering.md.
//
// # Wire format
//
//	[OpGuarded:1][compareCount:4 LE]([compareLen:4 LE][compare])*[inner command]
//
// Each compare is exactly the EncodeCompare encoding a transaction uses, and
// the inner command is an ordinary complete command, opcode included. The
// guard is a wrapper, not a field on each command, and that is what keeps
// the format backward compatible: a command with no guard is byte-for-byte
// the command it always was, and applies unconditionally exactly as before.
// Several inner payloads end in an unframed value (a KV Put's value is "the
// rest of the payload"), so a trailing optional guard could not be told apart
// from the value; a leading opcode can.
//
// # Mixed versions
//
// A replica running a binary that predates this opcode refuses the entry as
// an unknown opcode -- a refusal encoded into its result, not an apply fault
// -- and writes nothing, while the replicas that know the opcode apply the
// inner write. In a MULTI-NODE Raft group with mixed binaries a guarded
// write would therefore leave the replicas' state DIVERGED. It is never
// proposed to such a group: the cluster feature gate
// (pkg/cluster/featuregate, feature GuardedWrites) enables OpGuarded for a
// group only once every replica in it advertises support, and the one
// proposer of guarded writes (pkg/service's guardCommand) asks it first. A
// single-node group and a standalone node (a LocalBackend over the same
// engines) have one replica, so there is nothing to diverge from.
//
// The state machines that serve it are the four in pkg/statemachine
// (KVStateMachine, RegularStateMachine, FastStateMachine,
// ConcurrentStateMachine), pkg/store's PebbleStateMachine and
// MVCCStateMachine, and pkg/engine's KVEngine and MVCCEngine.
// pkg/backend's SimpleStateMachine does not, and refuses it in the result.
//
// # Compare semantics
//
// A guard is evaluated against the keyspace an ordinary KV Put writes to,
// through the same read each state machine's own CompareAndSwap uses, so the
// answer at a log position is identical on every replica. Only two targets
// are meaningful across every state machine that serves it:
//
//   - CompareTargetVersion: 0 when the key is absent, 1 when it is present.
//     Several state machines keep no per-key version counter, so presence is
//     the only honest reading, and "version == 0" is the idiom for "absent".
//   - CompareTargetValue: the stored bytes. An absent key equals only the
//     empty value, the rule transactions already use.
//
// Revision targets are refused at encode time (EncodeGuarded) and fail the
// guard closed at apply time if one is ever decoded.
// ---------------------------------------------------------------------------

// OpGuarded wraps another command with a guard. See the section comment
// above for the format and the evaluation rules.
const OpGuarded byte = 68

// Compare targets a guard understands. They share their values with the
// transaction compare targets (lock.CompareTarget) so a transaction's
// Compare converts to a guard compare without translation.
const (
	// CompareTargetVersion compares the key's presence: 0 absent, 1 present.
	CompareTargetVersion byte = 0

	// CompareTargetCreateRevision is a transaction-only target. A guard
	// refuses it.
	CompareTargetCreateRevision byte = 1

	// CompareTargetModRevision is a transaction-only target. A guard refuses
	// it.
	CompareTargetModRevision byte = 2

	// CompareTargetValue compares the stored value bytes.
	CompareTargetValue byte = 3
)

// Compare operators, shared with the transaction compare encoding
// (lock.CompareResult).
const (
	// CompareResultEqual holds when the operands are equal.
	CompareResultEqual byte = 0

	// CompareResultGreater holds when the stored operand is greater.
	CompareResultGreater byte = 1

	// CompareResultLess holds when the stored operand is less.
	CompareResultLess byte = 2

	// CompareResultNotEqual holds when the operands differ.
	CompareResultNotEqual byte = 3
)

// GuardReader returns the value a KV Get of key would return at the entry
// being applied: nil for an absent key, and a non-nil (possibly empty) slice
// for a present one. A non-nil error is a genuine read fault.
type GuardReader func(key []byte) ([]byte, error)

// GuardReadError reports that evaluating a guard could not read a key. It is
// a storage fault, never "the guard failed": folding a fault into a refusal
// would let replicas whose reads succeeded apply a write this one refused.
type GuardReadError struct {
	// Key is the key whose read failed.
	Key []byte

	// Err is the underlying read error.
	Err error
}

// Error returns a human-readable description of the read failure.
func (e *GuardReadError) Error() string {
	return "protocol: guard read of " + string(e.Key) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying read error.
func (e *GuardReadError) Unwrap() error {
	return e.Err
}

// ValidateGuard reports whether compares -- each produced by EncodeCompare --
// form a guard every serving state machine can evaluate: at least one compare, each
// decoding exactly, each on a target a guard understands.
func ValidateGuard(compares [][]byte) error {
	if len(compares) == 0 {
		return &InvalidCommandError{Reason: "guarded: no compares"}
	}
	for _, c := range compares {
		dc, n, err := DecodeCompare(c)
		if err != nil {
			return err
		}
		if n != len(c) {
			return &InvalidCommandError{Reason: "guarded: trailing bytes after compare"}
		}
		if !guardTargetSupported(dc.Target) {
			return &InvalidCommandError{Reason: "guarded: unsupported compare target"}
		}
	}
	return nil
}

// EncodeGuarded wraps inner -- a complete command, opcode included -- with
// the given compares, each produced by EncodeCompare.
//
// It refuses an invalid guard (see ValidateGuard), an empty inner command,
// and a guard nested inside another.
func EncodeGuarded(compares [][]byte, inner []byte) ([]byte, error) {
	if err := ValidateGuard(compares); err != nil {
		return nil, err
	}
	if len(inner) == 0 {
		return nil, &InvalidCommandError{Reason: "guarded: empty inner command"}
	}
	if inner[0] == OpGuarded {
		return nil, &InvalidCommandError{Reason: "guarded: nested guard"}
	}

	n, err := framedSlicesSize(compares)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, n, len(inner))
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	buf[0] = OpGuarded
	off := headerSize
	written, err := encodeFramedSlices(buf[off:], compares)
	if err != nil {
		return nil, err
	}
	off += written
	copy(buf[off:], inner)
	return buf, nil
}

// DecodeGuarded splits an OpGuarded payload (the bytes after the opcode)
// into its compares and its inner command. The inner command is returned
// whole -- opcode included, because some dispatchers decode base KV commands
// with their opcode -- and is never empty and never itself a guard.
func DecodeGuarded(payload []byte) ([]DecodedCompare, []byte, error) {
	if len(payload) < frameSize {
		return nil, nil, &InvalidCommandError{Reason: "guarded: missing compare count"}
	}
	count, err := decodeCount(binary.LittleEndian.Uint32(payload), len(payload)-frameSize, "guarded: compare")
	if err != nil {
		return nil, nil, err
	}
	if count == 0 {
		return nil, nil, &InvalidCommandError{Reason: "guarded: no compares"}
	}
	off := frameSize

	compares := make([]DecodedCompare, 0, preallocSize(count))
	for i := 0; i < count; i++ {
		if off+frameSize > len(payload) {
			return nil, nil, &InvalidCommandError{Reason: "guarded: truncated compare length"}
		}
		rawLen := binary.LittleEndian.Uint32(payload[off:])
		off += frameSize
		cLen, lerr := decodeLen(rawLen, len(payload)-off, "guarded: compare exceeds payload")
		if lerr != nil {
			return nil, nil, lerr
		}
		dc, used, derr := DecodeCompare(payload[off : off+cLen])
		if derr != nil {
			return nil, nil, derr
		}
		if used != cLen {
			return nil, nil, &InvalidCommandError{Reason: "guarded: trailing bytes after compare"}
		}
		compares = append(compares, dc)
		off += cLen
	}

	inner := payload[off:]
	if len(inner) == 0 {
		return nil, nil, &InvalidCommandError{Reason: "guarded: missing inner command"}
	}
	if inner[0] == OpGuarded {
		return nil, nil, &InvalidCommandError{Reason: "guarded: nested guard"}
	}
	return compares, inner, nil
}

// EvaluateGuard reports whether every compare holds against the state read
// returns. When one does not, refusedKey is the key of the first compare that
// failed; a read fault is returned as *GuardReadError and never folded into a
// refusal.
func EvaluateGuard(compares []DecodedCompare, read GuardReader) (refusedKey []byte, ok bool, err error) {
	for _, c := range compares {
		current, rerr := read(c.Key)
		if rerr != nil {
			return nil, false, &GuardReadError{Key: c.Key, Err: rerr}
		}
		if !CompareHolds(c, current) {
			return c.Key, false, nil
		}
	}
	return nil, true, nil
}

// OpenGuarded is the part of an OpGuarded handler every serving state machine
// shares: decode the payload, evaluate the guard against read, and either
// hand back the inner command to dispatch or the result a refused guard
// reports.
//
// Exactly one of the three outcomes is set:
//
//   - err != nil: the payload is malformed (*InvalidCommandError) or a read
//     faulted (*GuardReadError). Both are faults the caller classifies the
//     way it classifies any other decode or storage failure.
//   - refusal != nil: the guard did not hold. It is the sm.Result.Data the
//     entry reports (EncodeGuardRefusal); the entry applies successfully and
//     changes nothing, so consensus keeps advancing.
//   - otherwise inner is the complete command (opcode included) to
//     dispatch through the state machine's own handler table, at the same
//     revision the outer entry was given.
func OpenGuarded(payload []byte, read GuardReader) (inner, refusal []byte, err error) {
	compares, inner, err := DecodeGuarded(payload)
	if err != nil {
		return nil, nil, err
	}
	refusedKey, ok, err := EvaluateGuard(compares, read)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, EncodeGuardRefusal(refusedKey), nil
	}
	return inner, nil, nil
}

// guardTargetSupported reports whether a guard can evaluate target on every
// state machine.
func guardTargetSupported(target byte) bool {
	return target == CompareTargetVersion || target == CompareTargetValue
}

// CompareHolds evaluates one compare against current, the key's stored
// value (nil when the key is absent), for a store that keeps no per-key
// revisions: version is presence (0 or 1), an absent key equals only the
// empty value, and a revision target never holds. It is the rule both a
// guard and a revision-less state machine's transaction compare apply.
//
// An absent key is asymmetric under CompareTargetValue: current == nil
// holds against CompareResultEqual exactly when c.Value is also empty, but
// it NEVER holds against CompareResultNotEqual, Greater or Less, regardless
// of c.Value -- there is no stored byte slice for bytes.Compare to run
// against, only the one special case tested directly. In particular a
// NotEqual compare against a key that does not exist always evaluates to
// false, even for a non-empty c.Value, where an ordinary not-equal reading
// of "absent is not equal to this value" would expect true. A guard or
// transaction compare that must refuse WHEN a key is absent has to use
// CompareTargetVersion (0 means absent) instead of a NotEqual value
// compare for that case.
func CompareHolds(c DecodedCompare, current []byte) bool {
	switch c.Target {
	case CompareTargetVersion:
		var version int64
		if current != nil {
			version = 1
		}
		return CompareInt(c.Result, version, c.Version)
	case CompareTargetValue:
		if current == nil {
			return c.Result == CompareResultEqual && len(c.Value) == 0
		}
		return CompareBytes(c.Result, bytes.Compare(current, c.Value))
	default:
		// Revision targets carry no meaning without revisions. EncodeGuarded
		// refuses them; one that reaches here arrived by another path and
		// fails closed.
		return false
	}
}

// CompareBytes maps the result of bytes.Compare(stored, expected) onto a
// compare operator. An unknown operator never holds.
func CompareBytes(result byte, cmp int) bool {
	switch result {
	case CompareResultEqual:
		return cmp == 0
	case CompareResultGreater:
		return cmp > 0
	case CompareResultLess:
		return cmp < 0
	case CompareResultNotEqual:
		return cmp != 0
	default:
		return false
	}
}

// CompareInt applies a compare operator to (actual, expected). An unknown
// operator never holds.
func CompareInt(result byte, actual, expected int64) bool {
	switch result {
	case CompareResultEqual:
		return actual == expected
	case CompareResultGreater:
		return actual > expected
	case CompareResultLess:
		return actual < expected
	case CompareResultNotEqual:
		return actual != expected
	default:
		return false
	}
}
