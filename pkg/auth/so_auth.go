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
}

// NewSOAuthenticator constructs an SOAuthenticator. Both store and logger
// must be non-nil.
func NewSOAuthenticator(store kvstore.KVStore, auditLogger audit.AuditLogger, logger *slog.Logger) (*SOAuthenticator, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if auditLogger == nil {
		return nil, ErrNilAuditLogger
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	return &SOAuthenticator{store: store, audit: auditLogger, logger: logger}, nil
}

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
		a.audit.Emit(ctx, audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "set",
			Outcome:  audit.OutcomeFailure + ":hash",
		})
		return err
	}

	if err := a.store.Put(ctx, key, []byte(record)); err != nil {
		a.audit.Emit(ctx, audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "set",
			Outcome:  audit.OutcomeFailure + ":store",
		})
		return &OperationError{Op: "store put", Cause: err}
	}

	a.logger.Info("SO PIN committed", slog.String("tenantID", tenantID))
	a.audit.Emit(ctx, audit.AuditEvent{
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
		a.audit.Emit(ctx, audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "verify",
			Outcome:  audit.OutcomeFailure + ":not-set",
		})
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
		a.audit.Emit(ctx, audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "verify",
			Outcome:  audit.OutcomeFailure + ":invalid",
		})
		return ErrSOPINInvalid
	}

	a.audit.Emit(ctx, audit.AuditEvent{
		TenantID: tenantID,
		Resource: "so-pin",
		Action:   "verify",
		Outcome:  audit.OutcomeSuccess,
	})
	return nil
}

// RotateSOPIN replaces the committed SO PIN hash for the tenant. It verifies
// the old PIN before accepting the new one.
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
		a.audit.Emit(ctx, audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "rotate",
			Outcome:  audit.OutcomeFailure + ":hash",
		})
		return err
	}

	key := soPINCommitPrefix + tenantID
	if err := a.store.Put(ctx, key, []byte(record)); err != nil {
		a.audit.Emit(ctx, audit.AuditEvent{
			TenantID: tenantID,
			Resource: "so-pin",
			Action:   "rotate",
			Outcome:  audit.OutcomeFailure + ":store",
		})
		return &OperationError{Op: "store put", Cause: err}
	}

	a.logger.Info("SO PIN rotated", slog.String("tenantID", tenantID))
	a.audit.Emit(ctx, audit.AuditEvent{
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
