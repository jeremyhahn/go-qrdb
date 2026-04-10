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

	"github.com/awnumar/memguard"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

const credentialPrefix = "credentials/"

// CredentialService manages named credentials using a configurable strategy.
// The manual strategy stores credentials in-memory with blocking retrieval
// semantics. The barrier strategy delegates to a seal.StorageBarrier for
// encrypted persistence.
type CredentialService struct {
	barrier     *seal.StorageBarrier
	strategy    string
	logger      *slog.Logger
	mu          sync.RWMutex
	manualCreds map[string][]byte
	waitersMu   sync.Mutex
	waiters     map[string][]chan []byte
}

// NewCredentialService creates a new CredentialService with the given
// configuration, optional barrier, and logger.
//
// If cfg is nil or cfg.Strategy is empty, the manual strategy is used.
// The barrier parameter is required when using the barrier strategy and
// may be nil for the manual strategy. The logger must not be nil.
func NewCredentialService(cfg *Config, barrier *seal.StorageBarrier, logger *slog.Logger) (*CredentialService, error) {
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

	return &CredentialService{
		barrier:     barrier,
		strategy:    strategy,
		logger:      logger,
		manualCreds: make(map[string][]byte),
		waiters:     make(map[string][]chan []byte),
	}, nil
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
		return s.storeManual(name, value)
	case StrategyBarrier:
		return s.barrier.Put(ctx, credentialPrefix+name, value)
	default:
		return ErrInvalidStrategy
	}
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

	return s.submitManual(name, value)
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
		return s.deleteManual(name)
	case StrategyBarrier:
		return s.barrier.Delete(ctx, credentialPrefix+name)
	default:
		return ErrInvalidStrategy
	}
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

// storeManual stores a credential in the in-memory map. The value is
// copied to prevent caller mutations from affecting stored data.
func (s *CredentialService) storeManual(name string, value []byte) error {
	stored := make([]byte, len(value))
	copy(stored, value)

	s.mu.Lock()
	s.manualCreds[name] = stored
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
	// Fast path: credential already exists.
	s.mu.RLock()
	stored, ok := s.manualCreds[name]
	if ok {
		out := make([]byte, len(stored))
		copy(out, stored)
		s.mu.RUnlock()
		return out, nil
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
		return out, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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
	s.mu.Lock()
	if _, exists := s.manualCreds[name]; exists {
		s.mu.Unlock()
		return ErrCredentialAlreadySubmitted
	}

	stored := make([]byte, len(value))
	copy(stored, value)
	s.manualCreds[name] = stored
	s.mu.Unlock()

	// Notify waiters outside the main lock to avoid holding it during
	// channel sends.
	s.waitersMu.Lock()
	waiters := s.waiters[name]
	delete(s.waiters, name)
	s.waitersMu.Unlock()

	for _, ch := range waiters {
		out := make([]byte, len(value))
		copy(out, value)
		ch <- out
		close(ch)
	}

	s.logger.Debug("credential submitted",
		slog.String("name", name),
		slog.Int("waiters_notified", len(waiters)))

	return nil
}

// deleteManual removes a credential from the in-memory map after securely
// wiping the stored bytes.
func (s *CredentialService) deleteManual(name string) error {
	s.mu.Lock()
	stored, ok := s.manualCreds[name]
	if !ok {
		s.mu.Unlock()
		return ErrCredentialNotFound
	}
	memguard.WipeBytes(stored)
	delete(s.manualCreds, name)
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
