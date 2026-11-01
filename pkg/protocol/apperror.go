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
	"strconv"
)

// appErrorPrefix marks a state-machine result payload (sm.Result.Data) as an
// encoded APPLICATION error rather than an ordinary result payload.
//
// Raft semantics make this necessary: go-quicraft treats any non-nil error
// returned from a state machine's Update()/Apply() as a FATAL apply error that
// halts lastApplied and stalls every subsequent read and write on the group.
// Therefore a handler must NEVER return a Go error for an outcome that is a
// normal, deterministic consequence of valid (committed) client input — for
// example a unique-index violation, an invalid lease TTL, or an operation
// against a subsystem that is not configured on this state machine. Those
// outcomes are encoded into Data via EncodeAppError and surfaced to the client
// by the service layer (see service.checkResultError); only genuine
// non-deterministic corruption (storage IO failure) is returned as a Go error.
//
// The prefix is multi-byte and NUL-led so it cannot collide with the single
// flag bytes other handlers place in Data (e.g. the transaction success flag,
// which is a one-byte 0x00/0x01).
var appErrorPrefix = []byte("\x00qrdb.apperr\x00")

// AppErrorKind is WHAT KIND of refusal an encoded application error is.
//
// The kind has to be carried, not inferred. Every kind arrives as an encoded
// message, and without a marker the only way to tell "this state machine was
// not built to do that" from "your TTL is invalid" from "that key is taken" is
// to match on message text — which is what go-qrdb#141 split capability
// refusals out to stop, and what go-qrdb#557 finished by giving every other
// refusal a kind of its own.
//
// sm.Result.Data never crosses a node boundary — each replica decodes the
// result its own state machine produced — so extending this encoding is
// internal, not a wire change.
type AppErrorKind uint8

const (
	// AppErrorCorrupt — the committed command could not be decoded, or a
	// payload carries the error prefix with no kind this code writes. Either
	// way the bytes are not what a writer of this version produced.
	AppErrorCorrupt AppErrorKind = iota

	// AppErrorCapability — this state machine was not built to do that: an
	// opcode it has no handler for, or a subsystem it was constructed without.
	AppErrorCapability

	// AppErrorInvalid — the command was understood and refused on its merits:
	// an invalid lease TTL, a malformed time-series point.
	AppErrorInvalid

	// AppErrorConflict — the command would violate a uniqueness constraint.
	AppErrorConflict

	// AppErrorUnavailable — the subsystem that would serve the command is
	// closed on this replica.
	AppErrorUnavailable

	// AppErrorFault — the subsystem failed while applying the command.
	AppErrorFault

	// AppErrorGuardRefused — a guarded command's (OpGuarded) compare did not
	// hold at its own log position: the entry committed and applied nothing.
	// Distinct from AppErrorConflict (a uniqueness violation the command
	// itself would have caused) because a guard refusal is a lost
	// concurrency race the caller should re-read and retry, not a naming
	// collision -- the drop fence is the guard every Raft-backed write now
	// carries, so this is what a write into a keyspace DROP DATABASE just
	// reclaimed reports.
	AppErrorGuardRefused
)

// appErrorMarkers is each kind's marker, written after appErrorPrefix. A map
// rather than a switch: the kinds are values, the encoding is a lookup, and a
// kind added without a marker is a missing key the tests catch.
//
// AppErrorGuardRefused is deliberately NOT a row here: its payload is the raw
// key of the compare that failed, not a plain message, so it has its own
// encode/decode pair (EncodeGuardRefusal/DecodeGuardRefusal) and is
// synthesized into this kind only in DecodeAppErrorKind's fallback below --
// the same reason EncodeIndexOwnershipError's structured name+owner payload
// is not a row here either.
var appErrorMarkers = map[AppErrorKind][]byte{
	AppErrorCorrupt:     []byte("\x00corrupt\x00"),
	AppErrorCapability:  []byte("\x00cap\x00"),
	AppErrorInvalid:     []byte("\x00invalid\x00"),
	AppErrorConflict:    []byte("\x00conflict\x00"),
	AppErrorUnavailable: []byte("\x00unavail\x00"),
	AppErrorFault:       []byte("\x00fault\x00"),
}

// EncodeAppError encodes an application error of the given kind into a
// state-machine result payload. The returned bytes are placed in
// sm.Result.Data with a nil Go error so consensus continues to advance; the
// service layer decodes them with DecodeAppErrorKind and returns the typed
// error for the kind.
func EncodeAppError(kind AppErrorKind, msg string) []byte {
	marker := appErrorMarkers[kind]
	out := make([]byte, 0, len(appErrorPrefix)+len(marker)+len(msg))
	out = append(out, appErrorPrefix...)
	out = append(out, marker...)
	out = append(out, msg...)
	return out
}

// EncodeCapabilityError encodes an AppErrorCapability refusal. It is the
// encoder statemachine.ResolveApplyError reaches for, kept as a named entry
// point because an absent capability is the refusal most call sites raise.
func EncodeCapabilityError(msg string) []byte {
	return EncodeAppError(AppErrorCapability, msg)
}

// guardRefusalMarker distinguishes the application error a refused guard
// reports (see OpGuarded) from every other one. It carries the key of the
// compare that failed after the marker, so the service layer can say WHICH
// precondition stopped the write -- for the drop fence, which database is
// being dropped -- without parsing message text. Like ownershipMarker it
// sits after appErrorPrefix, so a reader that predates it still recognises
// the payload as an application error rather than an ordinary result.
var guardRefusalMarker = []byte("\x00guard\x00")

// EncodeGuardRefusal encodes the result of a guarded command whose guard did
// not hold: nothing was written, and key is the key of the compare that
// failed. See DecodeGuardRefusal.
func EncodeGuardRefusal(key []byte) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(guardRefusalMarker)+len(key))
	out = append(out, appErrorPrefix...)
	out = append(out, guardRefusalMarker...)
	out = append(out, key...)
	return out
}

// DecodeGuardRefusal reports whether data is a guard refusal and, if so,
// returns the key of the compare that failed.
func DecodeGuardRefusal(data []byte) (key []byte, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return nil, false
	}
	body := data[len(appErrorPrefix):]
	if !bytes.HasPrefix(body, guardRefusalMarker) {
		return nil, false
	}
	return body[len(guardRefusalMarker):], true
}

// guardRefusalMessage renders a guard refusal as the message a generic
// DecodeAppError/DecodeAppErrorKind caller reads.
func guardRefusalMessage(key []byte) string {
	return "write guard refused on key " + string(key)
}

// CompactRefusalReason is why the state machine refused an
// OpMVCCCompactPrefix: the command named a revision outside the range a
// compaction of its prefix can take.
type CompactRefusalReason uint8

const (
	// CompactRefusalInvalid -- the revision is below 1. Bound is unused.
	CompactRefusalInvalid CompactRefusalReason = iota + 1

	// CompactRefusalFuture -- the revision is above the store's current
	// revision. Bound is the current revision.
	CompactRefusalFuture

	// CompactRefusalCompacted -- the revision is at or below the compaction
	// floor already covering the prefix. Bound is that floor.
	CompactRefusalCompacted
)

// compactRefusalMarker distinguishes a compaction refusal from the other
// application errors. Like the guard and ownership refusals it carries a
// structured body -- [reason:1][requested:8 LE][bound:8 LE] -- because the
// service layer returns a typed error per reason (etcd's compacted and
// future-revision errors among them), and branching on message text is the
// defect go-qrdb#141 removed.
var compactRefusalMarker = []byte("\x00compact\x00")

// EncodeCompactRefusal encodes the refusal of a compaction to requested; see
// CompactRefusalReason for what bound carries.
func EncodeCompactRefusal(reason CompactRefusalReason, requested, bound int64) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(compactRefusalMarker)+1+8+8)
	out = append(out, appErrorPrefix...)
	out = append(out, compactRefusalMarker...)
	out = append(out, byte(reason))
	out = binary.LittleEndian.AppendUint64(out, uint64(requested)) //nolint:gosec // G115: two's-complement round trip of an int64
	out = binary.LittleEndian.AppendUint64(out, uint64(bound))     //nolint:gosec // G115: two's-complement round trip of an int64
	return out
}

// DecodeCompactRefusal reports whether data is a compaction refusal and, if
// so, its reason, the requested revision and the bound it violated.
func DecodeCompactRefusal(data []byte) (reason CompactRefusalReason, requested, bound int64, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return 0, 0, 0, false
	}
	body := data[len(appErrorPrefix):]
	if !bytes.HasPrefix(body, compactRefusalMarker) {
		return 0, 0, 0, false
	}
	body = body[len(compactRefusalMarker):]
	if len(body) != 1+8+8 {
		return 0, 0, 0, false
	}
	reason = CompactRefusalReason(body[0])
	requested = int64(binary.LittleEndian.Uint64(body[1:9])) //nolint:gosec // G115: two's-complement round trip of an int64
	bound = int64(binary.LittleEndian.Uint64(body[9:17]))    //nolint:gosec // G115: two's-complement round trip of an int64
	return reason, requested, bound, true
}

// compactRefusalMessage renders a compaction refusal as the message a
// generic DecodeAppError caller reads.
func compactRefusalMessage(reason CompactRefusalReason, requested, bound int64) string {
	msg := "mvcc compact: revision " + strconv.FormatInt(requested, 10)
	phrase, ok := compactRefusalPhrases[reason]
	if !ok {
		return msg + " was refused for an unknown reason " + strconv.Itoa(int(reason))
	}
	return msg + phrase(strconv.FormatInt(bound, 10))
}

// compactRefusalPhrases renders each refusal reason after the requested
// revision; bound is the reason's bound (see CompactRefusalReason).
var compactRefusalPhrases = map[CompactRefusalReason]func(bound string) string{
	CompactRefusalInvalid:   func(string) string { return " is invalid (must be at least 1)" },
	CompactRefusalFuture:    func(bound string) string { return " is a future revision (current " + bound + ")" },
	CompactRefusalCompacted: func(bound string) string { return " has been compacted (floor " + bound + ")" },
}

// LimitResource names the shard capacity a command was refused for. A
// configured cap travels in the command and is checked by the apply path
// against the state the command applies over, so every replica reaches the
// same verdict at the same log position whichever node proposed it
// (go-qrdb#1088).
type LimitResource uint8

const (
	// LimitIndexes -- the shard holds index.max_indexes indexes.
	LimitIndexes LimitResource = iota + 1

	// LimitMetrics -- the shard holds timeseries.max_metrics metric names.
	LimitMetrics
)

// limitRefusalMarker distinguishes a capacity refusal from the other
// application errors. Its body is [resource:1][max:4 LE]: the service layer
// returns a typed error per resource carrying the cap.
var limitRefusalMarker = []byte("\x00limit\x00")

// EncodeLimitRefusal encodes the refusal of a command that would take the
// shard past maxCount of resource.
func EncodeLimitRefusal(resource LimitResource, maxCount uint32) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(limitRefusalMarker)+1+4)
	out = append(out, appErrorPrefix...)
	out = append(out, limitRefusalMarker...)
	out = append(out, byte(resource))
	return binary.LittleEndian.AppendUint32(out, maxCount)
}

// DecodeLimitRefusal reports whether data is a capacity refusal and, if it
// is, the resource and the cap it was refused at.
func DecodeLimitRefusal(data []byte) (resource LimitResource, maxCount uint32, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return 0, 0, false
	}
	body := data[len(appErrorPrefix):]
	if !bytes.HasPrefix(body, limitRefusalMarker) {
		return 0, 0, false
	}
	body = body[len(limitRefusalMarker):]
	if len(body) != 1+4 {
		return 0, 0, false
	}
	return LimitResource(body[0]), binary.LittleEndian.Uint32(body[1:]), true
}

// limitRefusalNouns names each resource in a capacity refusal's message.
var limitRefusalNouns = map[LimitResource]string{
	LimitIndexes: "indexes",
	LimitMetrics: "metric names",
}

// limitRefusalMessage renders a capacity refusal as the message a generic
// DecodeAppError caller reads.
func limitRefusalMessage(resource LimitResource, maxCount uint32) string {
	noun, ok := limitRefusalNouns[resource]
	if !ok {
		noun = "resource " + strconv.Itoa(int(resource))
	}
	return "shard holds its limit of " + strconv.FormatUint(uint64(maxCount), 10) + " " + noun
}

// ownershipMarker distinguishes an index-ownership refusal (go-qrdb#426) from
// the other application errors. Like capabilityMarker it has to be carried
// rather than inferred from message text: the service layer turns it into a
// typed error that a collection teardown acts on -- an index it does not own
// is left alone -- and a caller branching on prose is the defect #141 removed.
//
// The body after the marker is "<name>\x00<recorded owner>", neither of which
// can contain NUL on the paths that reach it (both are length-prefixed
// strings decoded from a command, and the names the service layer builds are
// printable).
var ownershipMarker = []byte("\x00own\x00")

// EncodeIndexOwnershipError encodes the refusal of an index create or an
// owner-checked index drop whose named index is recorded under a different
// owner than the command claims. owner is the RECORDED owner, which the empty
// string spells for the explicit index API.
func EncodeIndexOwnershipError(name, owner string) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(ownershipMarker)+len(name)+1+len(owner))
	out = append(out, appErrorPrefix...)
	out = append(out, ownershipMarker...)
	out = append(out, name...)
	out = append(out, 0)
	out = append(out, owner...)
	return out
}

// DecodeIndexOwnershipError reports whether data carries an index-ownership
// refusal and, if so, the index name and its recorded owner.
func DecodeIndexOwnershipError(data []byte) (name, owner string, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return "", "", false
	}
	body := data[len(appErrorPrefix):]
	if !bytes.HasPrefix(body, ownershipMarker) {
		return "", "", false
	}
	body = body[len(ownershipMarker):]
	sep := bytes.IndexByte(body, 0)
	if sep < 0 {
		return string(body), "", true
	}
	return string(body[:sep]), string(body[sep+1:]), true
}

// indexOwnershipMessage renders an ownership refusal as the message a
// generic DecodeAppError caller reads.
func indexOwnershipMessage(name, owner string) string {
	if owner == "" {
		return "index " + name + " is owned by the explicit index API"
	}
	return "index " + name + " is owned by " + owner
}

// leaseNotFoundMarker distinguishes the refusal of a write naming a lease the
// state machine does not hold. Like ownershipMarker it carries a structured
// payload -- the lease ID in decimal -- so the service layer can return the
// same typed not-found error the pre-proposal lease check returns, without
// reading anything out of prose.
var leaseNotFoundMarker = []byte("\x00leasenf\x00")

// EncodeLeaseNotFoundError encodes the refusal of a write that names lease
// leaseID, which the state machine does not hold at apply time.
func EncodeLeaseNotFoundError(leaseID int64) []byte {
	id := strconv.FormatInt(leaseID, 10)
	out := make([]byte, 0, len(appErrorPrefix)+len(leaseNotFoundMarker)+len(id))
	out = append(out, appErrorPrefix...)
	out = append(out, leaseNotFoundMarker...)
	return append(out, id...)
}

// DecodeLeaseNotFoundError reports whether data is a lease-not-found refusal
// and, if so, the lease ID it names. A body that is not a decimal integer is
// not one this code wrote, and is not reported as one.
func DecodeLeaseNotFoundError(data []byte) (leaseID int64, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return 0, false
	}
	body := data[len(appErrorPrefix):]
	if !bytes.HasPrefix(body, leaseNotFoundMarker) {
		return 0, false
	}
	id, err := strconv.ParseInt(string(body[len(leaseNotFoundMarker):]), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// LogRefusalReason names the verifiable-log refusal a log-refusal payload
// carries (see EncodeLogRefusal).
type LogRefusalReason byte

// The verifiable-log refusals the state machine encodes under
// logRefusalMarker, each answered by the log service with its typed vlog
// error (go-qrdb#912).
const (
	// LogRefusalNotFound is a write naming a log that was never created.
	LogRefusalNotFound LogRefusalReason = iota + 1
	// LogRefusalExists is a create naming a log that already exists.
	LogRefusalExists
	// LogRefusalCheckpointExists is a checkpoint for a tree size that
	// already has one.
	LogRefusalCheckpointExists
	// LogRefusalExpireNotAllowed is an expiry on a log whose descriptor
	// does not allow it.
	LogRefusalExpireNotAllowed
)

// logRefusalKinds is the generic kind of each log refusal, the one the state
// machine's refusal table gives its vlog error, for a reader that does not
// know the reason. It is also the set of reasons DecodeLogRefusal accepts.
var logRefusalKinds = map[LogRefusalReason]AppErrorKind{
	LogRefusalNotFound:         AppErrorInvalid,
	LogRefusalExists:           AppErrorConflict,
	LogRefusalCheckpointExists: AppErrorConflict,
	LogRefusalExpireNotAllowed: AppErrorInvalid,
}

// logRefusalMarker distinguishes the refusal of a verifiable-log command
// (OpVerifiableLog) the log service answers with a typed vlog error. Its
// payload is the reason byte, the refused tree size as a little-endian
// uint64 (zero for a reason without one), then the refusal's message. Every
// such command names exactly one log, which the service layer already holds,
// so the payload need not carry it: the service rebuilds the typed error
// from the reason, the size and its own log ID, without reading anything out
// of prose.
var logRefusalMarker = []byte("\x00vlogref\x00")

// logRefusalHeader is the length of a log-refusal payload's reason and size.
const logRefusalHeader = 1 + 8

// EncodeLogRefusal encodes a verifiable-log refusal: its reason, the tree size
// it names (zero when it names none) and its message.
func EncodeLogRefusal(reason LogRefusalReason, size uint64, msg string) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(logRefusalMarker)+logRefusalHeader+len(msg))
	out = append(out, appErrorPrefix...)
	out = append(out, logRefusalMarker...)
	out = append(out, byte(reason))
	out = binary.LittleEndian.AppendUint64(out, size)
	return append(out, msg...)
}

// DecodeLogRefusal reports whether data is a verifiable-log refusal of a known
// reason and, if so, its reason, tree size and message.
func DecodeLogRefusal(data []byte) (reason LogRefusalReason, size uint64, msg string, ok bool) {
	body, ok := bytes.CutPrefix(data, appErrorPrefix)
	if !ok {
		return 0, 0, "", false
	}
	rest, ok := bytes.CutPrefix(body, logRefusalMarker)
	if !ok || len(rest) < logRefusalHeader {
		return 0, 0, "", false
	}
	reason = LogRefusalReason(rest[0])
	if _, known := logRefusalKinds[reason]; !known {
		return 0, 0, "", false
	}
	return reason, binary.LittleEndian.Uint64(rest[1:logRefusalHeader]), string(rest[logRefusalHeader:]), true
}

// DecodeAppError reports whether data carries an encoded application error and,
// if so, returns the error message with its kind marker stripped. Ordinary
// result payloads (including the one-byte transaction success flag) do not
// carry the prefix and return ok false.
func DecodeAppError(data []byte) (msg string, ok bool) {
	msg, _, ok = DecodeAppErrorKind(data)
	return msg, ok
}

// DecodeAppErrorKind decodes an application error and its kind.
//
// A payload that carries the prefix but no marker this code writes decodes
// as AppErrorCorrupt with the whole body as its message: it is an error —
// reading it as a result would report a refusal as a success — and it is not
// one this version produced.
func DecodeAppErrorKind(data []byte) (msg string, kind AppErrorKind, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return "", AppErrorCorrupt, false
	}
	body := data[len(appErrorPrefix):]
	for k, marker := range appErrorMarkers {
		if bytes.HasPrefix(body, marker) {
			return string(body[len(marker):]), k, true
		}
	}
	// An index-ownership refusal carries its own marker and payload (see
	// EncodeIndexOwnershipError). The index service decodes it into
	// *index.OwnershipError before it gets here; a generic reader gets its
	// message and the conflict kind -- the name is held by another owner.
	if name, owner, own := DecodeIndexOwnershipError(data); own {
		return indexOwnershipMessage(name, owner), AppErrorConflict, true
	}
	// A guard refusal carries its own marker and payload too (see
	// EncodeGuardRefusal). checkResultError decodes it directly into
	// *database.DroppingError or *service.WriteGuardRefusedError before it
	// gets here; a generic reader gets its rendered message and the
	// dedicated guard-refused kind.
	if key, refused := DecodeGuardRefusal(data); refused {
		return guardRefusalMessage(key), AppErrorGuardRefused, true
	}
	// A lease-not-found refusal carries the lease ID (see
	// EncodeLeaseNotFoundError). The service layer decodes it into its typed
	// not-found error first; a generic reader gets a message and the invalid
	// kind -- the request named a lease that does not exist.
	if id, nf := DecodeLeaseNotFoundError(data); nf {
		return "lease not found: " + strconv.FormatInt(id, 10), AppErrorInvalid, true
	}
	// A verifiable-log refusal carries its own marker and payload (see
	// EncodeLogRefusal). The log service decodes it into its typed vlog
	// error first; a generic reader gets its message and the kind the state
	// machine's refusal table gives that error.
	if reason, _, msg, refused := DecodeLogRefusal(data); refused {
		return msg, logRefusalKinds[reason], true
	}
	// A compaction refusal carries its own marker and payload too (see
	// EncodeCompactRefusal). checkResultError decodes it into the typed
	// service error for its reason before it gets here; a generic reader gets
	// its message and the invalid kind -- the command was refused on its
	// merits.
	if reason, requested, bound, refused := DecodeCompactRefusal(data); refused {
		return compactRefusalMessage(reason, requested, bound), AppErrorInvalid, true
	}
	// A capacity refusal carries its resource and cap (see
	// EncodeLimitRefusal). checkResultError decodes it into the typed limit
	// error for its resource first; a generic reader gets its message and
	// the conflict kind -- the shard's state, not the command, refused it.
	if resource, maxCount, refused := DecodeLimitRefusal(data); refused {
		return limitRefusalMessage(resource, maxCount), AppErrorConflict, true
	}
	return string(body), AppErrorCorrupt, true
}

// leaseInfoTag marks a state-machine Result.Data payload as the refreshed
// state of a lease, returned from the SAME apply that changed it (go-qrdb#275).
// KeepAlive used to answer every caller with a second, separately
// linearized read of the lease it had just reset, and that read's tail
// latency -- a ReadIndex round trip whenever the leader's local read lease
// was not valid -- was the keepalive path's heavy tail. The apply already
// computes the lease's post-update state; this lets the caller be
// answered from the committed result instead of asking the cluster again.
//
// The tag's first byte is non-zero, so no decoder gated on appErrorPrefix
// (which always begins 0x00) can mistake this for an application error.
var leaseInfoTag = byte('L')

// EncodeLeaseInfo encodes a lease's post-apply state for a state machine's
// Result.Data. Format:
// [tag:1='L'][id:8 LE][grantedTTL:8 LE][remainingTTL:8 LE][keyCount:4 LE]
// then, for each of keyCount keys: [keyLen:4 LE][key].
func EncodeLeaseInfo(id, grantedTTL, remainingTTL int64, keys [][]byte) []byte {
	size := 1 + 8 + 8 + 8 + 4
	for _, k := range keys {
		size += 4 + len(k)
	}
	buf := make([]byte, size)
	off := 0
	buf[off] = leaseInfoTag
	off++
	binary.LittleEndian.PutUint64(buf[off:], uint64(id))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(grantedTTL))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(remainingTTL))
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(keys))) //nolint:gosec // G115: bound by in-memory slice length
	off += 4
	for _, k := range keys {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(k))) //nolint:gosec // G115: bound by in-memory slice length
		off += 4
		off += copy(buf[off:], k)
	}
	return buf
}

// DecodeLeaseInfo decodes a payload written by EncodeLeaseInfo. ok is false
// for any payload EncodeLeaseInfo did not produce, including a truncated or
// over-long one, one whose key count exceeds what it holds, or an
// application-error payload (which never carries leaseInfoTag).
func DecodeLeaseInfo(data []byte) (id, grantedTTL, remainingTTL int64, keys [][]byte, ok bool) {
	const fixed = 1 + 8 + 8 + 8 + 4
	if len(data) < fixed || data[0] != leaseInfoTag {
		return 0, 0, 0, nil, false
	}
	off := 1
	id = int64(binary.LittleEndian.Uint64(data[off:])) //nolint:gosec // G115: two's-complement round trip
	off += 8
	grantedTTL = int64(binary.LittleEndian.Uint64(data[off:])) //nolint:gosec // G115: two's-complement round trip
	off += 8
	remainingTTL = int64(binary.LittleEndian.Uint64(data[off:])) //nolint:gosec // G115: two's-complement round trip
	off += 8
	count := binary.LittleEndian.Uint32(data[off:])
	off += 4
	// Every key carries at least its 4-byte length, so a count the
	// remaining bytes cannot hold is refused before it sizes anything.
	if uint64(count) > uint64(len(data)-off)/4 {
		return 0, 0, 0, nil, false
	}
	keys = make([][]byte, 0, count)
	for i := uint32(0); i < count; i++ {
		if off+4 > len(data) {
			return 0, 0, 0, nil, false
		}
		klen := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		if klen < 0 || off+klen > len(data) {
			return 0, 0, 0, nil, false
		}
		key := make([]byte, klen)
		copy(key, data[off:off+klen])
		keys = append(keys, key)
		off += klen
	}
	if off != len(data) {
		return 0, 0, 0, nil, false
	}
	return id, grantedTTL, remainingTTL, keys, true
}

// leaseKeepAliveNotFoundTag marks a state-machine Result.Data payload as a
// KeepAlive naming a lease the lessor does not (or no longer) hold.
//
// This is deliberately NOT EncodeLeaseNotFoundError's appErrorPrefix-gated
// encoding: a KeepAlive against an unknown lease is an idempotent apply
// success (replaying or duplicating a late keepalive must never stall Raft
// by refusing the apply, and must not be classified as an application
// error either -- see TestLeaseBinding_KeepAliveUnknownLeaseIsIdempotentSuccess
// and docs/leases/engineering.md's error-semantics table). The not-found
// outcome is still useful to the caller, so it travels back in Data for the
// service layer (go-qrdb#275) to translate into LeaseNotFoundError -- but
// only the service layer looks for this tag; DecodeAppError never matches
// it, the same way it never matches leaseInfoTag.
//
// The tag's first byte is non-zero, so no decoder gated on appErrorPrefix
// (which always begins 0x00) can mistake this for an application error.
var leaseKeepAliveNotFoundTag = byte('K')

// EncodeLeaseKeepAliveNotFound encodes a KeepAlive's not-found outcome for a
// state machine's Result.Data. Format: [tag:1='K'][id:8 LE].
func EncodeLeaseKeepAliveNotFound(id int64) []byte {
	buf := make([]byte, 1+8)
	buf[0] = leaseKeepAliveNotFoundTag
	binary.LittleEndian.PutUint64(buf[1:], uint64(id))
	return buf
}

// DecodeLeaseKeepAliveNotFound decodes a payload written by
// EncodeLeaseKeepAliveNotFound. ok is false for any payload this function
// did not produce, including a truncated one.
func DecodeLeaseKeepAliveNotFound(data []byte) (id int64, ok bool) {
	if len(data) != 1+8 || data[0] != leaseKeepAliveNotFoundTag {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(data[1:])), true //nolint:gosec // G115: two's-complement round trip
}
