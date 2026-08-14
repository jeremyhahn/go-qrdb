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
	// memguard enclaves — never as raw, garbage-collected heap bytes. The
	// plaintext is only materialized transiently (Open → copy → Destroy)
	// when a value is read.
	manualCreds map[string]*secure.Secret
	waitersMu   sync.Mutex
	waiters     map[string][]chan []byte

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
		waiters:     make(map[string][]chan []byte),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
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
	logger.Emit(ctx, audit.AuditEvent{
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
		if err := s.storeManual(name, value); err != nil {
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

// RetrieveCredential returns the credential value for the given name.
// For the manual strategy, this blocks until the credential is submitted
// or the context is cancelled. For the barrier strategy, the value is
// retrieved and decrypted from the StorageBarrier.
func (s *CredentialService) RetrieveCredential(ctx context.Context, name string) ([]byte, error) {
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
// blocking on RetrieveCredential are notified and receive a copy of the
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

	if err := s.submitManual(name, value); err != nil {
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
		if err := s.barrier.Delete(ctx, credentialPrefix+name); err != nil {
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

// newSecretCopy seals a copy of value into a memguard enclave. The caller's
// value is left intact (a throwaway copy is what NewSecret wipes).
func newSecretCopy(value []byte) (*secure.Secret, error) {
	tmp := make([]byte, len(value))
	copy(tmp, value)
	return secure.NewSecret(tmp) // wipes tmp
}

// openSecretCopy materializes a sealed secret's plaintext into a fresh
// caller-owned []byte, destroying the transient enclave buffer before return.
func openSecretCopy(sec *secure.Secret) ([]byte, error) {
	buf, err := sec.Open()
	if err != nil {
		return nil, err
	}
	defer buf.Destroy()
	out := make([]byte, buf.Size())
	copy(out, buf.Bytes())
	return out, nil
}

// storeManual seals a credential into an in-memory memguard enclave. Any prior
// enclave for the same name is destroyed first.
func (s *CredentialService) storeManual(name string, value []byte) error {
	sec, err := newSecretCopy(value)
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
func (s *CredentialService) retrieveManual(ctx context.Context, name string) ([]byte, error) {
	// Fast path: credential already exists. Materialize the enclave into a
	// transient copy while holding the read lock.
	s.mu.RLock()
	sec, ok := s.manualCreds[name]
	if ok {
		out, err := openSecretCopy(sec)
		s.mu.RUnlock()
		return out, err
	}
	s.mu.RUnlock()

	// Slow path: register a waiter and block.
	ch := make(chan []byte, 1)

	s.waitersMu.Lock()
	s.waiters[name] = append(s.waiters[name], ch)
	s.waitersMu.Unlock()

	s.logger.Debug("waiting for credential submission",
		slog.String("name", name))

	select {
	case value := <-ch:
		out := make([]byte, len(value))
		copy(out, value)
		// Wipe the channel-delivered plaintext copy; the caller keeps `out`.
		secure.Wipe(value)
		return out, nil
	case <-ctx.Done():
		// Deregister this waiter so a later submit does not copy the credential
		// into an abandoned buffered channel. If submit already raced and
		// delivered, drain and wipe the buffered plaintext.
		s.removeWaiter(name, ch)
		select {
		case value := <-ch:
			secure.Wipe(value)
		default:
		}
		return nil, ctx.Err()
	}
}

// removeWaiter removes a single waiter channel from the pending-waiters list
// for name. Used when a waiting retrieve is cancelled before a submit arrives.
func (s *CredentialService) removeWaiter(name string, ch chan []byte) {
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
func (s *CredentialService) retrieveBarrier(ctx context.Context, name string) ([]byte, error) {
	data, err := s.barrier.Get(ctx, credentialPrefix+name)
	if err != nil {
		return nil, ErrCredentialNotFound
	}
	return data, nil
}

// submitManual submits a credential to the in-memory map and notifies
// all registered waiters. Returns ErrCredentialAlreadySubmitted if the
// credential has already been stored.
func (s *CredentialService) submitManual(name string, value []byte) error {
	sec, err := newSecretCopy(value)
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
		out := make([]byte, len(value))
		copy(out, value)
		ch <- out
		close(ch)
	}
	s.waitersMu.Unlock()

	s.logger.Debug("credential submitted",
		slog.String("name", name),
		slog.Int("waiters_notified", len(waiters)))

	return nil
}

// deleteManual removes a credential and all of its non-current grace versions,
// destroying every backing memguard enclave.
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
