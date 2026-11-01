// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const credentialPrefix = "credentials/"

// CredentialService manages named credentials using a configurable strategy.
// The manual strategy stores credentials in-memory with blocking retrieval
// semantics. The barrier strategy delegates to a seal.StorageBarrier for
// encrypted persistence.
type CredentialService struct {
	barrier  *seal.StorageBarrier
	strategy string
	logger   *slog.Logger
	mu       sync.RWMutex
	// manualCreds holds manual-strategy credential values at rest inside
	// go-enclave sealed enclaves (secure.Secret) — never as raw,
	// garbage-collected heap bytes. The plaintext is only materialized
	// transiently (Open → copy → Destroy) when a value is read.
	manualCreds map[string]*secure.Secret
	waitersMu   sync.Mutex
	waiters     map[string][]chan waiterDelivery

	// Manual-strategy versioned-rotation state. Guarded by mu.
	// manualVersions stores non-current grace-period values keyed by
	// [name][version], also enclave-protected. manualMeta stores the
	// metadata sidecar (current version + grace history) keyed by name.
	// Both are lazily initialised on the first rotate; legacy untracked
	// credentials remain in manualCreds with no entry here.
	manualVersions map[string]map[uint64]*secure.Secret
	manualMeta     map[string]*credentialMeta

	// audit is an optional compliance-grade audit logger. When set, every
	// credential mutation (store, submit, rotate, delete, gc) emits an
	// audit event capturing the acting identity from context. A nil logger
	// disables emission. It is read via auditLogger()/SetAuditLogger under
	// mu so the daemon can attach it after construction (the seal subsystem
	// builds the service before the compliance logger exists).
	audit audit.AuditLogger

	// testHooks are interleaving seams for this package's tests; every
	// field is nil in production.
	testHooks serviceTestHooks
}

// serviceTestHooks lets a test run code at fixed points of retrieveManual to
// force an interleaving with a concurrent submit deterministically.
type serviceTestHooks struct {
	// afterManualMiss runs after the fast-path existence check misses and
	// before the waiter is registered.
	afterManualMiss func()
	// afterWaiterRegistered runs after the waiter is registered and before
	// the existence re-check.
	afterWaiterRegistered func()
}

// waiterDelivery is what submitManual hands a blocked retriever: its own
// protected copy of the submitted value, or the error that prevented making
// one (an exhausted locked-memory budget). Exactly one field is set.
type waiterDelivery struct {
	buf *secure.Buffer
	err error
}

// Option configures a CredentialService at construction time.
type Option func(*CredentialService)

// WithAuditLogger attaches a compliance-grade audit logger so credential
// mutations leave an audit trail. A nil logger disables emission, so the
// option is safe to apply unconditionally.
func WithAuditLogger(logger audit.AuditLogger) Option {
	return func(s *CredentialService) {
		s.audit = logger
	}
}

// NewCredentialService creates a new CredentialService with the given
// configuration, optional barrier, and logger.
//
// If cfg is nil or cfg.Strategy is empty, the manual strategy is used.
// The barrier parameter is required when using the barrier strategy and
// may be nil for the manual strategy. The logger must not be nil.
func NewCredentialService(cfg *Config, barrier *seal.StorageBarrier, logger *slog.Logger, opts ...Option) (*CredentialService, error) {
	if logger == nil {
		return nil, ErrNilLogger
	}

	strategy := StrategyManual
	if cfg != nil && cfg.Strategy != "" {
		strategy = cfg.Strategy
	}

	if !validStrategies[strategy] {
		return nil, ErrInvalidStrategy
	}

	if strategy == StrategyBarrier && barrier == nil {
		return nil, ErrNilBarrier
	}

	s := &CredentialService{
		barrier:     barrier,
		strategy:    strategy,
		logger:      logger,
		manualCreds: make(map[string]*secure.Secret),
		waiters:     make(map[string][]chan waiterDelivery),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// ForBarrier returns a barrier-strategy CredentialService over barrier that
// shares s's logger and audit logger. It is how a tenant's named-credential
// store is derived from the node-wide one: the same versioned rotation API,
// persisted in the tenant barrier's own storage and encrypted under that
// barrier's key. It returns ErrNilBarrier when barrier is nil.
func (s *CredentialService) ForBarrier(barrier *seal.StorageBarrier) (*CredentialService, error) {
	return NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, s.logger,
		WithAuditLogger(s.auditLogger()))
}

// SetAuditLogger attaches (or replaces) the compliance audit logger after
// construction. It exists for wiring paths where the service is built before
// the daemon's compliance logger is available (e.g. the seal subsystem). A
// nil logger disables emission. Safe to call once during boot before the
// service handles requests.
func (s *CredentialService) SetAuditLogger(logger audit.AuditLogger) {
	s.mu.Lock()
	s.audit = logger
	s.mu.Unlock()
}

// auditLogger returns the configured audit logger under mu.
func (s *CredentialService) auditLogger() audit.AuditLogger {
	s.mu.RLock()
	l := s.audit
	s.mu.RUnlock()
	return l
}

// emitAudit records a successful credential mutation. It captures the acting
// identity (Subject + TenantID) from the request context and is a no-op when
// no audit logger is configured. Emission is best-effort per the AuditLogger
// contract and never affects the mutation outcome.
func (s *CredentialService) emitAudit(ctx context.Context, action, name string, attrs map[string]any) {
	logger := s.auditLogger()
	if logger == nil {
		return
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["strategy"] = s.strategy
	var actor, tenantID string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
		tenantID = id.TenantID
	}
	audit.EmitBestEffort(ctx, logger, "credentials", audit.AuditEvent{
		Actor:      actor,
		TenantID:   tenantID,
		Resource:   "credential:" + name,
		Action:     action,
		Outcome:    audit.OutcomeSuccess,
		Attributes: attrs,
	})
}

// StoreCredential persists a credential under the given name.
// For the manual strategy, the value is stored in-memory. For the barrier
// strategy, the value is encrypted and stored via the StorageBarrier.
func (s *CredentialService) StoreCredential(ctx context.Context, name string, value []byte) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateValue(value); err != nil {
		return err
	}

	switch s.strategy {
	case StrategyManual:
		if err := s.storeManual(ctx, name, value); err != nil {
			return err
		}
	case StrategyBarrier:
		if err := s.barrier.Put(ctx, credentialPrefix+name, value); err != nil {
			return err
		}
	default:
		return ErrInvalidStrategy
	}

	s.emitAudit(ctx, "store", name, nil)
	return nil
}

// RetrieveCredentialBuffer returns the credential value for the given name in
// protected memory. For the manual strategy, this blocks until the credential
// is submitted or the context is cancelled. For the barrier strategy, the value
// is retrieved and decrypted from the StorageBarrier.
//
// The returned buffer is a fresh copy owned by the caller, who MUST Destroy it
// once done, on every path. The value never exists as a plain []byte inside
// this package: it is copied out only by a caller that chooses to, and the one
// place the stack does so on purpose is the secret-bearing wire DTO of each
// transport (see docs/credentials/architecture.md, "Protected-memory
// boundary").
func (s *CredentialService) RetrieveCredentialBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	switch s.strategy {
	case StrategyManual:
		return s.retrieveManual(ctx, name)
	case StrategyBarrier:
		return s.retrieveBarrier(ctx, name)
	default:
		return nil, ErrInvalidStrategy
	}
}

// SubmitCredential submits a credential for the manual strategy. Waiters
// blocking on RetrieveCredentialBuffer are notified and receive a copy of the
// value. For the barrier strategy, this delegates to StoreCredential.
func (s *CredentialService) SubmitCredential(ctx context.Context, name string, value []byte) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateValue(value); err != nil {
		return err
	}

	if s.strategy != StrategyManual {
		return s.StoreCredential(ctx, name, value)
	}

	if err := s.submitManual(ctx, name, value); err != nil {
		return err
	}
	s.emitAudit(ctx, "submit", name, nil)
	return nil
}

// DeleteCredential removes the credential with the given name.
// For the manual strategy, the credential bytes are securely wiped
// before removal. For the barrier strategy, the key is deleted from
// the StorageBarrier.
func (s *CredentialService) DeleteCredential(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}

	switch s.strategy {
	case StrategyManual:
		if err := s.deleteManual(name); err != nil {
			return err
		}
	case StrategyBarrier:
		if err := s.deleteBarrierAll(ctx, name); err != nil {
			return err
		}
	default:
		return ErrInvalidStrategy
	}

	s.emitAudit(ctx, "delete", name, nil)
	return nil
}

// Strategy returns the active credential strategy name.
func (s *CredentialService) Strategy() string {
	return s.strategy
}

// AutoUnsealAvailable reports whether the credential strategy supports
// automatic unseal. The manual strategy requires human interaction and
// returns false; the barrier strategy returns true.
func (s *CredentialService) AutoUnsealAvailable() bool {
	return s.strategy != StrategyManual
}

// newSecretCopy seals a copy of value into a go-enclave sealed enclave. The
// caller's value is left intact (a throwaway copy is what NewSecret wipes).
func newSecretCopy(ctx context.Context, value []byte) (*secure.Secret, error) {
	tmp := make([]byte, len(value))
	copy(tmp, value)
	return secure.NewSecret(ctx, tmp) // wipes tmp
}

// newBufferCopy copies value into a fresh protected Buffer. The caller's value
// is left intact (a throwaway copy is what NewBufferFromBytes wipes).
func newBufferCopy(value []byte) (*secure.Buffer, error) {
	tmp := make([]byte, len(value))
	copy(tmp, value)
	return secure.NewBufferFromBytes(tmp) // wipes tmp
}

// bufferFromHeap moves a plaintext slice that a lower layer (the storage
// barrier's decrypt) allocated into a protected Buffer and wipes the slice, so
// the heap copy lives only as long as this call.
func bufferFromHeap(value []byte) (*secure.Buffer, error) {
	return secure.NewBufferFromBytes(value) // wipes value
}

// storeManual seals a credential into an in-memory go-enclave sealed enclave.
// Any prior enclave for the same name is destroyed first.
func (s *CredentialService) storeManual(ctx context.Context, name string, value []byte) error {
	sec, err := newSecretCopy(ctx, value)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if old := s.manualCreds[name]; old != nil {
		old.Destroy()
	}
	s.manualCreds[name] = sec
	s.mu.Unlock()

	s.logger.Debug("credential stored",
		slog.String("name", name),
		slog.String("strategy", StrategyManual))

	return nil
}

// retrieveManual retrieves a credential from the in-memory map. If the
// credential has not yet been submitted, a waiter channel is registered
// and the call blocks until the credential is submitted or the context
// is cancelled.
func (s *CredentialService) retrieveManual(ctx context.Context, name string) (*secure.Buffer, error) {
	// Fast path: credential already exists. Open the enclave into a fresh
	// protected buffer while holding the read lock.
	if out, ok, err := s.openManual(ctx, name); ok {
		return out, err
	}
	if s.testHooks.afterManualMiss != nil {
		s.testHooks.afterManualMiss()
	}

	// Slow path: register a waiter and block.
	ch := make(chan waiterDelivery, 1)

	s.waitersMu.Lock()
	s.waiters[name] = append(s.waiters[name], ch)
	s.waitersMu.Unlock()
	if s.testHooks.afterWaiterRegistered != nil {
		s.testHooks.afterWaiterRegistered()
	}

	// Re-check after registering. A submit that stored the credential
	// between the fast-path miss and the registration above has already
	// notified the waiters that existed then, so this one would never be
	// woken. A submit that stores after this check finds the waiter
	// registered, because submitManual stores before it notifies.
	if out, ok, err := s.openManual(ctx, name); ok {
		// Deregister, then drop any copy a submit delivered in the
		// meantime: removeWaiter and the delivery both run under
		// waitersMu, so once it returns no further delivery can arrive.
		s.removeWaiter(name, ch)
		select {
		case d := <-ch:
			d.buf.Destroy()
		default:
		}
		return out, err
	}

	s.logger.Debug("waiting for credential submission",
		slog.String("name", name))

	select {
	case d := <-ch:
		// The delivered buffer is this waiter's own protected copy; the caller
		// keeps it.
		return d.buf, d.err
	case <-ctx.Done():
		// Deregister this waiter so a later submit does not copy the credential
		// into an abandoned buffered channel. If submit already raced and
		// delivered, drain and destroy the buffered protected copy.
		s.removeWaiter(name, ch)
		select {
		case d := <-ch:
			d.buf.Destroy()
		default:
		}
		return nil, ctx.Err()
	}
}

// openManual opens the stored manual credential name into a fresh protected
// buffer under the read lock. ok reports whether the credential exists.
func (s *CredentialService) openManual(ctx context.Context, name string) (out *secure.Buffer, ok bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sec, ok := s.manualCreds[name]
	if !ok {
		return nil, false, nil
	}
	out, err = sec.Open(ctx)
	return out, true, err
}

// removeWaiter removes a single waiter channel from the pending-waiters list
// for name. Used when a waiting retrieve is cancelled before a submit arrives.
func (s *CredentialService) removeWaiter(name string, ch chan waiterDelivery) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()
	waiters := s.waiters[name]
	for i, w := range waiters {
		if w == ch {
			s.waiters[name] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(s.waiters[name]) == 0 {
		delete(s.waiters, name)
	}
}

// retrieveBarrier retrieves a credential from the StorageBarrier. If the
// underlying storage returns an error, ErrCredentialNotFound is returned.
func (s *CredentialService) retrieveBarrier(ctx context.Context, name string) (*secure.Buffer, error) {
	data, err := s.barrier.Get(ctx, credentialPrefix+name)
	if err != nil {
		return nil, ErrCredentialNotFound
	}
	return bufferFromHeap(data)
}

// submitManual submits a credential to the in-memory map and notifies
// all registered waiters. Returns ErrCredentialAlreadySubmitted if the
// credential has already been stored.
func (s *CredentialService) submitManual(ctx context.Context, name string, value []byte) error {
	sec, err := newSecretCopy(ctx, value)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if _, exists := s.manualCreds[name]; exists {
		s.mu.Unlock()
		sec.Destroy()
		return ErrCredentialAlreadySubmitted
	}
	s.manualCreds[name] = sec
	s.mu.Unlock()

	// Notify waiters while holding waitersMu so a concurrent cancellation
	// (removeWaiter) cannot interleave with the send. The waiter channels are
	// buffered (cap 1), so the sends never block.
	s.waitersMu.Lock()
	waiters := s.waiters[name]
	delete(s.waiters, name)
	for _, ch := range waiters {
		buf, copyErr := newBufferCopy(value)
		ch <- waiterDelivery{buf: buf, err: copyErr}
		close(ch)
	}
	s.waitersMu.Unlock()

	s.logger.Debug("credential submitted",
		slog.String("name", name),
		slog.Int("waiters_notified", len(waiters)))

	return nil
}

// deleteManual removes a credential and all of its non-current grace versions,
// destroying every backing go-enclave sealed enclave.
func (s *CredentialService) deleteManual(name string) error {
	s.mu.Lock()
	sec, ok := s.manualCreds[name]
	if !ok {
		s.mu.Unlock()
		return ErrCredentialNotFound
	}
	sec.Destroy()
	delete(s.manualCreds, name)

	// Destroy any grace-period version enclaves and drop the metadata sidecar
	// so the contract "delete removes all non-current versions" holds.
	for _, vsec := range s.manualVersions[name] {
		vsec.Destroy()
	}
	delete(s.manualVersions, name)
	delete(s.manualMeta, name)
	s.mu.Unlock()

	s.logger.Debug("credential deleted",
		slog.String("name", name),
		slog.String("strategy", StrategyManual))

	return nil
}

// validateName checks that the credential name is non-empty after trimming.
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrEmptyCredentialName
	}
	return nil
}

// validateValue checks that the credential value is non-nil and non-empty.
func validateValue(value []byte) error {
	if len(value) == 0 {
		return ErrEmptyCredentialValue
	}
	return nil
}
