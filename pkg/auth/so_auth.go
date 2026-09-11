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

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	// soPINCommitPrefix is the KV key prefix for hash-committed SO PINs.
	// Full key: __sys:so-pin-commit/{tenantID}.
	soPINCommitPrefix = "__sys:so-pin-commit/"

	// argon2idTime is the Argon2id iteration count.
	argon2idTime = 3

	// argon2idMemory is the Argon2id memory in KiB (64 MiB).
	argon2idMemory = 64 * 1024

	// argon2idThreads is the Argon2id parallelism.
	argon2idThreads = 4

	// argon2idKeyLen is the derived key length in bytes.
	argon2idKeyLen = 32

	// pbkdf2Iterations is the PBKDF2-SHA256 iteration count per NIST SP 800-132.
	pbkdf2Iterations = 600_000

	// pbkdf2KeyLen is the PBKDF2 derived key length in bytes.
	pbkdf2KeyLen = 32

	// pinHashRecordLen is the total length of the stored hash record:
	// kdfTag (1 byte) + ':' + saltHex (64 bytes) + ':' + hashHex (64 bytes) = 131 bytes.
	//
	// kdfTags:
	//   'A' = Argon2id
	//   'P' = PBKDF2-SHA256
)

var (
	// ErrSOPINNotSet is returned when VerifySOPIN is called before SetSOPIN.
	ErrSOPINNotSet = errors.New("auth: SO PIN not set for tenant")

	// ErrSOPINInvalid is returned when the presented PIN does not match.
	ErrSOPINInvalid = errors.New("auth: SO PIN verification failed")

	// ErrSOPINEmptyTenant is returned when the tenantID is empty.
	ErrSOPINEmptyTenant = errors.New("auth: tenant ID is required for SO PIN operations")

	// ErrSOPINEmptyPIN is returned when the PIN bytes are empty.
	ErrSOPINEmptyPIN = errors.New("auth: SO PIN must not be empty")

	// ErrSOPINAlreadySet is returned when SetSOPIN is called for a tenant
	// that already has a PIN committed.
	ErrSOPINAlreadySet = errors.New("auth: SO PIN already set for tenant; use RotateSOPIN to change it")

	// ErrNilStore is returned when a nil KVStore is provided.
	ErrNilStore = errors.New("auth: nil kv store provided")
)

// SOAuthenticator verifies SO dual-factor authentication:
// mTLS identity (established externally) + in-band PIN on every request.
//
// The PIN is hash-committed at SO bootstrap via SetSOPIN using Argon2id
// (standard mode) or PBKDF2-SHA256 (FIPS mode). The hash record is
// persisted at __sys:so-pin-commit/{tenantID} via Raft so all nodes can
// verify. Verification is constant-time.
//
// PIN material is held in *secure.Buffer instances and destroyed
// immediately after hashing.
type SOAuthenticator struct {
	store  kvstore.KVStore
	audit  audit.AuditLogger
	logger *slog.Logger

	// auditFailure is what VerifySOPIN does when a verification outcome
	// cannot be recorded (go-qrdb#445).
	//
	// Its ZERO VALUE is audit.FailClosed, so an SOAuthenticator built without
	// WithSOAuditFailurePolicy -- by a test, an embedder, or a future
	// constructor nobody remembered to update -- refuses rather than reports a
	// second factor that passed with nothing in the trail to say so.
	//
	// It is the SAME operator lever the authorization middleware reads,
	// auth.audit_failure_policy, resolved once at the boot and handed to both.
	// Two keys would reintroduce the split this field exists to remove.
	auditFailure audit.FailurePolicy
}

// SOOption configures an SOAuthenticator at construction.
type SOOption func(*SOAuthenticator)

// WithSOAuditFailurePolicy selects what VerifySOPIN does when it cannot record
// a verification outcome. Not applying it leaves audit.FailClosed, the zero
// value.
//
// It takes the value an embedder already resolved from
// auth.audit_failure_policy for the authorization middleware. There is no
// second configuration key: go-qrdb#445 ruled ONE posture across both
// assertions, because "two postures with one lever" is what left the most
// privileged action in the system on the weaker one.
func WithSOAuditFailurePolicy(p audit.FailurePolicy) SOOption {
	return func(a *SOAuthenticator) { a.auditFailure = p }
}

// NewSOAuthenticator constructs an SOAuthenticator. store, auditLogger and
// logger must all be non-nil.
//
// With no options the audit failure posture is audit.FailClosed: a PIN
// verification whose outcome cannot be recorded is refused.
func NewSOAuthenticator(store kvstore.KVStore, auditLogger audit.AuditLogger, logger *slog.Logger, opts ...SOOption) (*SOAuthenticator, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if auditLogger == nil {
		return nil, ErrNilAuditLogger
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	a := &SOAuthenticator{store: store, audit: auditLogger, logger: logger}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// soAuthComponent names this subsystem on the audit-failure counters and on
// the *audit.GateError VerifySOPIN returns.
const soAuthComponent = "so-auth"

var (
	// ErrNilAuditLogger is returned when a nil audit logger is provided to auth constructors.
	ErrNilAuditLogger = errors.New("auth: nil audit logger provided")

	// ErrNilLogger is returned when a nil slog logger is provided to auth constructors.
	ErrNilLogger = errors.New("auth: nil logger provided")
)

// SetSOPIN commits the hash of newPIN for the given tenant. It uses Argon2id
// in standard mode or PBKDF2-SHA256 in FIPS mode (see fipsEnabled).
//
// newPIN is wrapped in a *secure.Buffer and destroyed after hashing.
// Returns ErrSOPINAlreadySet if a PIN is already committed for the tenant.
//
// ITS AUDIT RECORDS ARE BEST-EFFORT, AND THAT IS THE CONSIDERED ANSWER, not an
// omission go-qrdb#445 missed. SetSOPIN is an administrative MUTATION, not a
// gate: nothing is waiting on its record for permission to proceed. The
// success record is written AFTER store.Put has already replicated the commit
// through Raft, so propagating a failed write would report a completed
// operation as failed -- the operator retries, gets ErrSOPINAlreadySet, and
// now believes the PIN is both unset and already set. There is no rollback an
// emitter may perform (a compensating delete is a second Raft write on the
// strength of a failed audit write, which is the fallback this feature
// forbids), so the honest posture is the one EmitBestEffort documents: log,
// count qrdb_audit_write_failures_total{component="so-auth"}, and let the
// truthful result stand. The two failure records are post-hoc for the same
// reason -- the operation is already being refused and a lost record cannot
// make it more refused.
//
// VerifySOPIN is the opposite shape and is gated; see emitVerify.
func (a *SOAuthenticator) SetSOPIN(ctx context.Context, tenantID string, newPIN []byte) error {
	if tenantID == "" {
		return ErrSOPINEmptyTenant
	}
	if len(newPIN) == 0 {
		return ErrSOPINEmptyPIN
	}

	// Wrap PIN in a secure.Buffer; NewBufferFromBytes wipes newPIN on
	// entry so the caller's slice is zeroed.
	pinBuf, err := newPINBuffer(newPIN)
	if err != nil {
		return err
	}
	defer pinBuf.Destroy()

	// Reject if already committed.
	key := soPINCommitPrefix + tenantID
	existing, err := a.store.Get(ctx, key)
	if err != nil {
		return &OperationError{Op: "store get", Cause: err}
	}
	if existing != nil {
		return ErrSOPINAlreadySet
	}

	// The borrow spans the KDF, not just the slice-header read: Bytes() left
	// the PIN's pages unmapped-able by a memguard failure elsewhere in the
	// process while the hash was still running over them (go-qrdb#74).
	//
	// Safe to hold because hashPIN and verifyPIN are pure KDFs in this file —
	// PBKDF2 or Argon2 over ordinary heap slices, with cryptoRandRead for the
	// salt. Neither allocates protected memory, which is the condition for
	// holding purgeMu shared across a call.
	var record string
	err = pinBuf.WithBytes(func(pin []byte) error {
		var herr error
		record, herr = hashPIN(pin)
		return herr
	})
	if err != nil {
		audit.EmitBestEffort(ctx, a.audit, "so-auth", audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "set",
			Outcome:  audit.OutcomeFailure + ":hash",
		})
		return err
	}

	if err := a.store.Put(ctx, key, []byte(record)); err != nil {
		audit.EmitBestEffort(ctx, a.audit, "so-auth", audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "set",
			Outcome:  audit.OutcomeFailure + ":store",
		})
		return &OperationError{Op: "store put", Cause: err}
	}

	a.logger.Info("SO PIN committed", slog.String("tenantID", tenantID))
	audit.EmitBestEffort(ctx, a.audit, "so-auth", audit.AuditEvent{
		TenantID: tenantID,
		Resource: "so-pin",
		Action:   "set",
		Outcome:  audit.OutcomeSuccess,
	})
	return nil
}

// VerifySOPIN checks whether presentedPIN matches the committed hash for the
// tenant. Verification is constant-time to prevent timing attacks. The PIN
// material is wrapped in a *secure.Buffer and destroyed after use.
//
// IT FAILS CLOSED ON AN UNRECORDABLE OUTCOME (go-qrdb#445). A nil return is
// the second factor passing, and what the caller does next is rekey a barrier,
// enrol a custodian or sign a CSR. So the verification record GATES that
// operation exactly as the authorization decision one layer out does, and a
// factor that passes with nothing in the trail to say so is precisely the
// event an audit trail exists to capture.
//
// When the record cannot be written and the posture is audit.FailClosed --
// the default -- the returned error is an *audit.GateError wrapping the sink's
// own failure, NOT ErrSOPINInvalid. The distinction is load-bearing: a caller
// that flattened them would tell a security officer holding a perfectly good
// PIN that the PIN was wrong, on a node whose audit disk was full. Every
// transport answers it 503 / UNAVAILABLE via
// middleware.ClassifyAuthError -- "retry", never "you are not permitted".
func (a *SOAuthenticator) VerifySOPIN(ctx context.Context, tenantID string, presentedPIN []byte) error {
	if tenantID == "" {
		return ErrSOPINEmptyTenant
	}
	if len(presentedPIN) == 0 {
		return ErrSOPINEmptyPIN
	}

	pinBuf, err := newPINBuffer(presentedPIN)
	if err != nil {
		return err
	}
	defer pinBuf.Destroy()

	key := soPINCommitPrefix + tenantID
	record, err := a.store.Get(ctx, key)
	if err != nil {
		return &OperationError{Op: "store get", Cause: err}
	}
	if record == nil {
		if gateErr := a.emitVerify(ctx, tenantID, audit.OutcomeFailure+":not-set"); gateErr != nil {
			return gateErr
		}
		return ErrSOPINNotSet
	}

	// The borrow spans the KDF, not just the slice-header read: Bytes() left
	// the PIN's pages unmapped-able by a memguard failure elsewhere in the
	// process while the hash was still running over them (go-qrdb#74).
	//
	// Safe to hold because hashPIN and verifyPIN are pure KDFs in this file —
	// PBKDF2 or Argon2 over ordinary heap slices, with cryptoRandRead for the
	// salt. Neither allocates protected memory, which is the condition for
	// holding purgeMu shared across a call.
	if err := pinBuf.WithBytes(func(pin []byte) error {
		return verifyPIN(pin, string(record))
	}); err != nil {
		if gateErr := a.emitVerify(ctx, tenantID, audit.OutcomeFailure+":invalid"); gateErr != nil {
			return gateErr
		}
		return ErrSOPINInvalid
	}

	return a.emitVerify(ctx, tenantID, audit.OutcomeSuccess)
}

// emitVerify records one SO-PIN verification outcome and reports whether the
// record landed, per the configured posture.
//
// EVERY verification outcome goes through here, success and refusal alike, and
// all three are GATING rather than post-hoc: nothing has committed at this
// point, and what follows a nil return is the caller performing the most
// privileged operation the system offers. Before go-qrdb#445 all three were
// audit.EmitBestEffort, so an SO factor could pass, its record could fail to
// write, and the caller was told the factor passed.
//
// The refusal branches are gated too, and deliberately. Making only the
// success fail closed would be two rules inside one function -- "we answer
// only what we could record, except when we were going to refuse anyway" --
// and the caller is refused either way; what changes is that it is told to
// RETRY (the node could not record) rather than told its PIN was wrong, which
// under an audit outage is the honest answer and the one a recovering operator
// can act on.
//
// The refusal is NOT itself recorded: the only sink is the one that just
// failed, and writing to it again is the retry this feature forbids. It rides
// qrdb_audit_gate_failures_total{component="so-auth"} and an ERROR log line
// instead -- see audit.EmitGated.
func (a *SOAuthenticator) emitVerify(ctx context.Context, tenantID, outcome string) error {
	return audit.EmitGated(ctx, a.audit, soAuthComponent, a.auditFailure, audit.AuditEvent{
		TenantID: tenantID,
		Resource: "so-pin",
		Action:   "verify",
		Outcome:  outcome,
	})
}

// RotateSOPIN replaces the committed SO PIN hash for the tenant. It verifies
// the old PIN before accepting the new one.
//
// THE GATE IS FAIL-CLOSED AND THE MUTATION RECORD IS NOT, which is the same
// split SetSOPIN documents rather than an inconsistency. The old-PIN check
// delegates to VerifySOPIN, so an unrecordable verification refuses the
// rotation with an *audit.GateError before anything is written. The records of
// the rotation ITSELF are post-hoc: the failure ones accompany an error the
// caller is already getting, and the success one is written after store.Put
// has committed the new hash, where refusing would leave the caller believing
// the old PIN still works.
func (a *SOAuthenticator) RotateSOPIN(ctx context.Context, tenantID string, oldPIN, newPIN []byte) error {
	if tenantID == "" {
		return ErrSOPINEmptyTenant
	}
	if len(newPIN) == 0 {
		return ErrSOPINEmptyPIN
	}

	// Verify the old PIN first. VerifySOPIN emits its own failure audit events.
	if err := a.VerifySOPIN(ctx, tenantID, oldPIN); err != nil {
		return err
	}

	newBuf, err := newPINBuffer(newPIN)
	if err != nil {
		return err
	}
	defer newBuf.Destroy()

	// The borrow spans the KDF, not just the slice-header read: Bytes() left
	// the PIN's pages unmapped-able by a memguard failure elsewhere in the
	// process while the hash was still running over them (go-qrdb#74).
	//
	// Safe to hold because hashPIN and verifyPIN are pure KDFs in this file —
	// PBKDF2 or Argon2 over ordinary heap slices, with cryptoRandRead for the
	// salt. Neither allocates protected memory, which is the condition for
	// holding purgeMu shared across a call.
	var record string
	err = newBuf.WithBytes(func(pin []byte) error {
		var herr error
		record, herr = hashPIN(pin)
		return herr
	})
	if err != nil {
		audit.EmitBestEffort(ctx, a.audit, "so-auth", audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "rotate",
			Outcome:  audit.OutcomeFailure + ":hash",
		})
		return err
	}

	key := soPINCommitPrefix + tenantID
	if err := a.store.Put(ctx, key, []byte(record)); err != nil {
		audit.EmitBestEffort(ctx, a.audit, "so-auth", audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "rotate",
			Outcome:  audit.OutcomeFailure + ":store",
		})
		return &OperationError{Op: "store put", Cause: err}
	}

	a.logger.Info("SO PIN rotated", slog.String("tenantID", tenantID))
	audit.EmitBestEffort(ctx, a.audit, "so-auth", audit.AuditEvent{
		TenantID: tenantID,
		Resource: "so-pin",
		Action:   "rotate",
		Outcome:  audit.OutcomeSuccess,
	})
	return nil
}

// fipsEnabled reports whether FIPS 140 mode is in effect. Detection is
// delegated to the stack-wide predicate in go-quicraft/pkg/seal (re-exported
// as seal.FIPSEnabled), which keys off crypto/fips140.Enabled first and
// treats GOFIPS140=off as disabled — the previous local implementation read
// only the environment variable and misread "off" as FIPS-ENABLED.
func fipsEnabled() bool {
	return quicraftseal.FIPSEnabled()
}

// hashPIN derives a hash record from the PIN bytes. The record format is:
//
//	kdfTag:saltHex:hashHex
//
// where kdfTag is 'A' (Argon2id) or 'P' (PBKDF2-SHA256).
// A 32-byte random salt is generated for each call.
// hashPIN and verifyPIN are variables for the same reason cryptoRandRead above
// is: they are the only points inside the PIN borrow a test can observe. The
// property under test is a LOCK LIFETIME — that the borrow is held across the
// KDF rather than merely across the slice-header read — and a lifetime is only
// visible from inside the window it covers (go-qrdb#74).
// pinBuffer is the protected-memory buffer a PIN lives in while it is hashed:
// the borrow, and the destroy that ends its life.
//
// An interface rather than *secure.Buffer so a test can observe that the KDF
// runs INSIDE the borrow callback. That is the property these three methods
// were changed for, and it is not observable from outside the window it covers
// — which is why the previous Bytes() form survived every success and error
// test in this package (go-qrdb#74).
type pinBuffer interface {
	WithBytes(fn func([]byte) error) error
	Destroy()
}

// newPINBuffer seals a PIN into protected memory, consuming src. A variable
// for the same reason as the KDFs below: it is the injection point for the
// borrow observation.
var newPINBuffer = func(src []byte) (pinBuffer, error) {
	return secure.NewBufferFromBytes(src)
}

var hashPIN = hashPINImpl

var verifyPIN = verifyPINImpl

func hashPINImpl(pin []byte) (string, error) {
	salt := make([]byte, 32)
	if _, err := cryptoRandRead(salt); err != nil {
		return "", &OperationError{Op: "generate salt", Cause: err}
	}

	var (
		hash   []byte
		kdfTag byte
	)

	if fipsEnabled() {
		// PBKDF2-SHA256 per NIST SP 800-132.
		hash = pbkdf2.Key(pin, salt, pbkdf2Iterations, pbkdf2KeyLen, sha256.New)
		kdfTag = 'P'
	} else {
		// Argon2id: 3 iterations, 64 MiB, 4 threads.
		hash = argon2.IDKey(pin, salt, argon2idTime, argon2idMemory, argon2idThreads, argon2idKeyLen)
		kdfTag = 'A'
	}

	return fmt.Sprintf("%c:%s:%s",
		kdfTag,
		hex.EncodeToString(salt),
		hex.EncodeToString(hash),
	), nil
}

// verifyPIN checks presentedPIN against the stored hash record using
// constant-time comparison.
func verifyPINImpl(presentedPIN []byte, record string) error {
	if len(record) < 3 {
		return ErrSOPINInvalid
	}
	kdfTag := record[0]
	rest := record[2:] // skip "X:"

	// Split "saltHex:hashHex".
	colonIdx := -1
	for i, c := range rest {
		if c == ':' {
			colonIdx = i
			break
		}
	}
	if colonIdx < 0 {
		return ErrSOPINInvalid
	}

	saltHex := rest[:colonIdx]
	hashHex := rest[colonIdx+1:]

	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return ErrSOPINInvalid
	}
	storedHash, err := hex.DecodeString(hashHex)
	if err != nil {
		return ErrSOPINInvalid
	}

	var derived []byte
	switch kdfTag {
	case 'P':
		derived = pbkdf2.Key(presentedPIN, salt, pbkdf2Iterations, pbkdf2KeyLen, sha256.New)
	case 'A':
		derived = argon2.IDKey(presentedPIN, salt, argon2idTime, argon2idMemory, argon2idThreads, argon2idKeyLen)
	default:
		return ErrSOPINInvalid
	}

	// Constant-time comparison.
	if subtle.ConstantTimeCompare(derived, storedHash) != 1 {
		return ErrSOPINInvalid
	}
	return nil
}

// cryptoRandRead is the package-level randomness source. It can be replaced
// in tests to simulate read failures.
var cryptoRandRead = func(b []byte) (int, error) {
	return io.ReadFull(rand.Reader, b)
}
