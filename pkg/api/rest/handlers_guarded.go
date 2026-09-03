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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	quicraftproto "github.com/jeremyhahn/go-quicraft/pkg/proto"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Guarded write verbs (go-qrdb#126)
// ---------------------------------------------------------------------------
//
// The guarded verbs are POST endpoints with a JSON body rather than
// conditional headers on PUT/DELETE /kv/{key}, matching the family this
// repository already uses for compound KV operations (/kv/compact,
// /kv/restore, /tx, /lock/acquire). They are POST because each carries a
// request document that does not fit a path and a header, and because none of
// them is idempotent in the sense PUT promises: a second PutIfAbsent with the
// same body returns a DIFFERENT answer, and that difference is the whole
// product.
//
// Values travel as JSON []byte fields, which encoding/json renders as base64.
// That is deliberate and differs from the transaction endpoint, which sends
// values as JSON strings: a guard compares BYTES, so a value that does not
// survive a UTF-8 round trip would make CompareAndDelete refuse a release that
// should have succeeded — a lock that can be taken and never let go. The KV
// data plane is already binary-safe (PUT /kv/{key} carries the raw body), and
// the guarded plane has to be too or it cannot guard what the plane stores.
//
// Each handler calls the service layer's ATOMIC method. None of them reads the
// key first: a guarded verb emulated as get-then-put over HTTP is not a
// guarded verb, it is a race with a longer window.

// putIfAbsentRequest is the JSON payload for POST /kv/put-if-absent.
type putIfAbsentRequest struct {
	Key   string `json:"key"`
	Value []byte `json:"value,omitempty"`
}

// payloadBytes is the DECODED size of this request: the key plus every value
// it carries, summed. Summed, not per field — budgeting each field
// independently multiplies the ceiling by the field count, which is how the
// first cut of this bound came to admit 267 MiB.
func (r putIfAbsentRequest) payloadBytes() int { return len(r.Key) + len(r.Value) }

// keyBytes is the key's length, checked against the protocol's two-byte
// length frame.
func (r putIfAbsentRequest) keyBytes() int { return len(r.Key) }

// commandOverheadBytes is what this verb's encoder adds around the key and
// value inside one Raft entry.
func (r putIfAbsentRequest) commandOverheadBytes() int { return putIfAbsentOverheadBytes }

// putIfAbsentResponse reports whether the value was stored: true when the key
// was absent and the value was written, false when it was already present.
type putIfAbsentResponse struct {
	Stored bool `json:"stored"`
}

// compareAndDeleteRequest is the JSON payload for POST /kv/compare-and-delete.
type compareAndDeleteRequest struct {
	Key      string `json:"key"`
	Expected []byte `json:"expected,omitempty"`
}

// payloadBytes is the DECODED size of this request. See putIfAbsentRequest.
func (r compareAndDeleteRequest) payloadBytes() int { return len(r.Key) + len(r.Expected) }

// keyBytes is the key's length. See putIfAbsentRequest.
func (r compareAndDeleteRequest) keyBytes() int { return len(r.Key) }

// commandOverheadBytes is what this verb's encoder adds. See putIfAbsentRequest.
func (r compareAndDeleteRequest) commandOverheadBytes() int { return compareAndDeleteOverheadBytes }

// compareAndDeleteResponse reports whether the key was deleted: true when the
// stored value equalled expected and the key was removed, false otherwise.
type compareAndDeleteResponse struct {
	Deleted bool `json:"deleted"`
}

// compareAndSwapRequest is the JSON payload for POST /kv/compare-and-swap.
type compareAndSwapRequest struct {
	Key      string `json:"key"`
	Expected []byte `json:"expected,omitempty"`
	Next     []byte `json:"next,omitempty"`
}

// payloadBytes is the DECODED size of this request. It carries TWO values, and
// they are summed against one ceiling rather than each getting its own.
func (r compareAndSwapRequest) payloadBytes() int {
	return len(r.Key) + len(r.Expected) + len(r.Next)
}

// keyBytes is the key's length. See putIfAbsentRequest.
func (r compareAndSwapRequest) keyBytes() int { return len(r.Key) }

// commandOverheadBytes is what this verb's encoder adds. It is the largest of
// the three: a swap frames `expected` explicitly so `next` can follow it.
func (r compareAndSwapRequest) commandOverheadBytes() int { return compareAndSwapOverheadBytes }

// guardedRequest is what decodeGuardedRequest can bound. The single method is
// the aggregate decoded size, which is the only thing the bound needs from a
// request and the only thing each of the three shapes reports differently.
type guardedRequest interface {
	payloadBytes() int
	keyBytes() int
	commandOverheadBytes() int
}

// compareAndSwapResponse reports whether the value was replaced: true when the
// stored value equalled expected and next was written, false otherwise —
// including when the key was absent, since a swap does not create.
type compareAndSwapResponse struct {
	Swapped bool `json:"swapped"`
}

// handleKVPutIfAbsent handles POST /kv/put-if-absent -- stores the value only
// when the key is absent, evaluating the condition inside the state machine.
// A declined store is a 200 carrying {"stored": false}, NOT an error status:
// losing the race is the normal, expected outcome of a claim and the caller
// asked a question rather than issuing a command that failed.
func (s *Server) handleKVPutIfAbsent(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeGuardedRequest[putIfAbsentRequest](w, r, maxGuardedRequestBytes, guardedEntryCeiling)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "kv"})
		}
		return
	}

	stored, err := kvSvc.PutIfAbsent(ctx, req.Key, req.Value)
	if err != nil {
		if s.tryForwardWrite(w, r, guardedForwardBody(req), err) {
			return
		}
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, putIfAbsentResponse{Stored: stored})
}

// handleKVCompareAndDelete handles POST /kv/compare-and-delete -- removes the
// key only when its stored value still equals expected. It is the release half
// of PutIfAbsent. A refused delete is a 200 carrying {"deleted": false}, for
// the same reason a declined store is.
func (s *Server) handleKVCompareAndDelete(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeGuardedRequest[compareAndDeleteRequest](w, r, maxGuardedRequestBytes, guardedEntryCeiling)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "kv"})
		}
		return
	}

	deleted, err := kvSvc.CompareAndDelete(ctx, req.Key, req.Expected)
	if err != nil {
		if s.tryForwardWrite(w, r, guardedForwardBody(req), err) {
			return
		}
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, compareAndDeleteResponse{Deleted: deleted})
}

// handleKVCompareAndSwap handles POST /kv/compare-and-swap -- replaces the
// key's value only when its stored value still equals expected. An absent key
// is left absent: a swap is not a create.
func (s *Server) handleKVCompareAndSwap(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeGuardedRequest[compareAndSwapRequest](w, r, maxGuardedRequestBytes, guardedEntryCeiling)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	kvSvc := s.kvSvcFor(w, ctx)
	if kvSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "kv"})
		}
		return
	}

	swapped, err := kvSvc.CompareAndSwap(ctx, req.Key, req.Expected, req.Next)
	if err != nil {
		if s.tryForwardWrite(w, r, guardedForwardBody(req), err) {
			return
		}
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, compareAndSwapResponse{Swapped: swapped})
}

// ---------------------------------------------------------------------------
// Request bounds
// ---------------------------------------------------------------------------
//
// Without a bound, any caller holding kv:put or kv:delete could make the
// server allocate an entire body before JSON decoding or any service-layer
// check ran — an HTTP read timeout limits how LONG a request may take, never
// how many BYTES it may claim, so a fast client inside the window allocates
// whatever it likes. http.MaxBytesReader is the mechanism /batch/import
// already uses in this package.
//
// There are TWO bounds, and the split is the point:
//
//   - maxGuardedRequestBytes is a coarse pre-decode guard on the ENCODED body.
//     It answers the memory question and nothing else.
//   - the per-verb payload ceiling, computed in decodeGuardedRequest from each
//     verb's own command framing, is the precise limit — checked after
//     decoding, on the key plus every value SUMMED.
//
// The first cut had only the coarse one and budgeted 100 MiB per FIELD, so a
// CompareAndSwap could claim ~267 MiB of encoded JSON and ~200 MiB of decoded
// values. Budgeting per field multiplies the ceiling by the field count, which
// is what the aggregate check exists to prevent.

// guardedEntryCeiling is the size of one Raft log entry, and the only limit
// the system actually enforces on one of these requests: a guarded verb
// becomes exactly ONE entry. pkg/protocol records the same dependency and
// pkg/service sizes its import ceilings under it. A request larger than this
// cannot be proposed no matter what any transport accepts, so admitting it
// buys nothing but the allocation.
const guardedEntryCeiling = int(quicraftproto.MaxEntrySize)

// The per-verb command framing, MEASURED from the encoders rather than
// estimated. The first cut used one shared constant with three 4-byte prefixes
// and 256 bytes of invented "slack", which made the ceiling 269 bytes too low
// — so REST returned 413 for requests that still encode under
// proto.MaxEntrySize and succeed over gRPC, unix and QUIC. A pessimistic
// margin looks harmless and is not: on a limit that must match another
// transport's, every invented byte is a request the other transports carry and
// this one refuses.
//
// Where a verb has more than one encoder, the SMALLEST framing is taken, so
// this layer never refuses what some backend could still encode. A backend
// whose encoder frames more (the MVCC PutIfAbsent carries an 8-byte lease
// field) then refuses at encode time, exactly as it does for every other
// transport.
const (
	// kv.EncodePutIfAbsent: [op:1][keyLen:2][key][value].
	putIfAbsentOverheadBytes = 1 + 2

	// protocol.EncodeCompareAndDelete: [op:1][keyLen:2][key][expected],
	// with expected as the unframed remainder.
	compareAndDeleteOverheadBytes = 1 + 2

	// protocol.EncodeCompareAndSwap: [op:1][keyLen:2][key][expectedLen:4]
	// [expected][next], with next as the unframed remainder.
	compareAndSwapOverheadBytes = 1 + 2 + protocol.FrameSize
)

// maxGuardedKeyBytes is the largest key a guarded command can carry, and it is
// a PROTOCOL ceiling rather than a policy choice: the encoders frame the key
// with a two-byte length prefix (protocol.EncodeCompareAndSwap and its
// siblings call checkUint16Len on it), so a longer key cannot be encoded at
// all. Refusing it here turns a propose-time encode failure into a 413 that
// says which field was too long.
const maxGuardedKeyBytes = protocol.MaxUint16Field

// maxGuardedRequestBytes bounds the ENCODED request body, and it does exactly
// one job: nothing unbounded is ever read. It is deliberately NOT a model of
// how any field encodes.
//
// That distinction is the whole design here, and it was arrived at the hard
// way. Four earlier versions of this constant tried to be both the memory
// guard AND the admissibility decision, by pre-budgeting each field's wire
// expansion. Every one was wrong, alternating direction: a per-field budget
// admitted requests gRPC refuses; a base64 model refused legal keys, because a
// key is a JSON string and a control byte in one expands sixfold; an invented
// 256-byte margin refused payloads every other transport accepts. Modelling
// escaping would have fixed the third instance and left a fourth arithmetic to
// get wrong — multi-byte UTF-8, then whatever the next field type is.
//
// A single number cannot be both conservative enough never to reject a legal
// request and tight enough to bound memory. So it is not asked to be. This
// bounds BYTES, coarsely; decodeGuardedRequest decides LEGALITY afterwards
// from the exact decoded sizes, where nothing has to be estimated.
//
// Twice the entry ceiling is the coarse figure. A legal request's body cannot
// approach it — base64 costs 4/3 and JSON escaping at most 6 on a key already
// capped at 64 KiB — and no argument here depends on that arithmetic being
// exactly right, because it is not what decides admissibility.
// TestMaxGuardedRequestBytes_AdmitsTheWorstLegalRequest builds the largest
// request the precise checks accept and measures its encoded size against this
// bound, so the relationship is asserted rather than computed.
const maxGuardedRequestBytes = int64(2 * guardedEntryCeiling)

// decodeGuardedRequest reads, bounds and decodes a guarded verb's JSON body.
//
// It streams through a json.Decoder rather than reading the whole body first,
// and it does NOT retain the encoded bytes: the leader-forwarding path
// re-encodes the decoded request instead (see guardedForwardBody). Re-encoding
// is safe here and re-reading is not free — the guard is carried in typed
// []byte fields that round-trip through base64 exactly, and the schema is the
// server's own, so a re-encode is the same request. Keeping the raw body alive
// across the service call would hold a second copy of every request on the
// common path to pay for the rare one.
//
// limit and entryCeiling are parameters rather than direct reads of the
// constants so both bounds can be exercised at their edges without a test
// allocating the production bound's worth of memory to prove a memory guard.
// Every caller passes maxGuardedRequestBytes and guardedEntryCeiling.
//
// It reports ok=false once it has written the error response, so callers
// return immediately.
func decodeGuardedRequest[T guardedRequest](w http.ResponseWriter, r *http.Request, limit int64, entryCeiling int) (req T, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	dec := json.NewDecoder(r.Body)

	// An unknown member is REFUSED, not ignored. Ignoring one is a fail-open
	// parse path on a state-changing endpoint: a body like
	// {"key":"claim","vale":"..."} decodes with value nil and PutIfAbsent
	// stores an EMPTY value and reports success, and a misspelled `expected`
	// makes CompareAndDelete compare against nil — a typo performing a
	// mutation, which is the same shape as the trailing-data hole below.
	//
	// This was deliberately left off for one round on a forward-compatibility
	// argument: an older server refusing a newer client's optional field. That
	// argument does not apply here. The repository is pre-release and promises
	// no wire compatibility (CLAUDE.md's no-cruft policy, and pkg/protocol
	// says the same about its own frame widths), so there is no older server
	// to protect and the cost is paid entirely by malformed input.
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		// A body over the wire bound is 413, not 400: the request was
		// well-formed enough to read and the caller can act on it by sending
		// less. The limit travels in the error so a caller can choose a size
		// that will be accepted rather than guessing.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorJSON(w, http.StatusRequestEntityTooLarge,
				&RequestTooLargeError{Limit: tooLarge.Limit})
			return req, false
		}
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return req, false
	}

	// A JSON decoder stops at the end of the FIRST top-level value and leaves
	// whatever follows unread, so `{"key":"a",...}{"key":"b"}` would decode as
	// the first object and this handler would perform a guarded MUTATION on
	// malformed input and report success. That is a fail-open parse path on
	// unverified input, on a write endpoint. Requiring EOF closes it.
	//
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeErrorJSON(w, http.StatusBadRequest, &TrailingDataError{})
		return req, false
	}

	// The key ceiling, checked before the aggregate so an over-long key is
	// reported as an over-long KEY rather than as an over-large request.
	if n := req.keyBytes(); n > maxGuardedKeyBytes {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge,
			&KeyTooLargeError{Size: n, Limit: maxGuardedKeyBytes})
		return req, false
	}

	// The precise limit, computed PER VERB from that verb's own command
	// framing. One shared constant would have to assume the worst framing and
	// would then refuse, on the other two verbs, requests their encoders
	// accept — the same class of defect as inventing a margin. The wire bound
	// above is deliberately loose, and a body can sit under it while decoding
	// to more content than one Raft entry can carry.
	payloadLimit := entryCeiling - req.commandOverheadBytes()
	if n := req.payloadBytes(); n > payloadLimit {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge,
			&PayloadTooLargeError{Size: n, Limit: payloadLimit})
		return req, false
	}

	return req, true
}

// guardedForwardBody re-encodes a decoded guarded request for the
// leader-forwarding hop. It returns nil when encoding fails, which
// tryForwardWrite treats as "no body to forward" — these shapes cannot fail to
// marshal, and failing closed to the local error response is the right answer
// if one ever did.
//
// forwardToLeader wraps the returned slice in a bytes.Reader, which references
// it rather than copying, so the forwarding path holds ONE copy of the body and
// only on the not-leader path.
func guardedForwardBody(req any) []byte {
	body, err := json.Marshal(req)
	if err != nil {
		slog.Error("rest: re-encoding a guarded request for leader forwarding failed", "error", err)
		return nil
	}
	return body
}
