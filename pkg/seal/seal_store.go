// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
)

// SealStore provides a persistent store for named secrets that are
// encrypted at rest using the StorageBarrier's sealing mechanism.
// All secrets are stored under a configurable key prefix and are
// transparently encrypted/decrypted by the barrier.
type SealStore struct {
	barrier *StorageBarrier
	prefix  string
	logger  *slog.Logger
	closed  atomic.Bool
}

// NewSealStore creates a new SealStore backed by the given barrier.
// The prefix is prepended to all storage keys for namespace isolation.
func NewSealStore(barrier *StorageBarrier, prefix string, logger *slog.Logger) (*SealStore, error) {
	if barrier == nil {
		return nil, ErrNilSystemBarrier
	}
	if prefix == "" {
		prefix = "sealstore/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &SealStore{
		barrier: barrier,
		prefix:  prefix,
		logger:  logger,
	}, nil
}

// Put encrypts and stores a named secret in the barrier. The name
// must not be empty. The barrier encrypts the secret transparently.
func (s *SealStore) Put(ctx context.Context, name string, secret []byte) error {
	if name == "" {
		return ErrInvalidSecretName
	}
	if s.barrier.IsSealed() {
		return &SealStoreError{Op: "put", Key: name, Err: ErrSealStoreBarrierSealed}
	}
	key := s.prefix + name
	if err := s.barrier.Put(ctx, key, secret); err != nil {
		return &SealStoreError{Op: "put", Key: name, Err: err}
	}
	s.logger.Debug("seal store: secret stored",
		slog.String("name", name))
	return nil
}

// Get retrieves and decrypts a named secret from the barrier.
// Returns ErrSealStoreSecretNotFound if the secret does not exist.
func (s *SealStore) Get(ctx context.Context, name string) ([]byte, error) {
	if name == "" {
		return nil, ErrInvalidSecretName
	}
	if s.barrier.IsSealed() {
		return nil, &SealStoreError{Op: "get", Key: name, Err: ErrSealStoreBarrierSealed}
	}
	key := s.prefix + name
	data, err := s.barrier.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, &SealStoreError{Op: "get", Key: name, Err: ErrSealStoreSecretNotFound}
		}
		return nil, &SealStoreError{Op: "get", Key: name, Err: err}
	}
	return data, nil
}

// Delete removes a named secret from the barrier.
// Returns ErrSealStoreSecretNotFound if the secret does not exist.
func (s *SealStore) Delete(ctx context.Context, name string) error {
	if name == "" {
		return ErrInvalidSecretName
	}
	if s.barrier.IsSealed() {
		return &SealStoreError{Op: "delete", Key: name, Err: ErrSealStoreBarrierSealed}
	}
	key := s.prefix + name
	exists, err := s.barrier.Exists(ctx, key)
	if err != nil {
		return &SealStoreError{Op: "delete", Key: name, Err: err}
	}
	if !exists {
		return &SealStoreError{Op: "delete", Key: name, Err: ErrSealStoreSecretNotFound}
	}
	if err := s.barrier.Delete(ctx, key); err != nil {
		return &SealStoreError{Op: "delete", Key: name, Err: err}
	}
	s.logger.Debug("seal store: secret deleted",
		slog.String("name", name))
	return nil
}

// List returns all stored secret names (without the key prefix).
// The returned names are sorted lexicographically.
func (s *SealStore) List(ctx context.Context) ([]string, error) {
	if s.barrier.IsSealed() {
		return nil, &SealStoreError{Op: "list", Err: ErrSealStoreBarrierSealed}
	}
	keys, err := s.barrier.List(ctx, s.prefix)
	if err != nil {
		return nil, &SealStoreError{Op: "list", Err: err}
	}
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		name := strings.TrimPrefix(key, s.prefix)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Reseal reads a secret, deletes the old ciphertext, and re-encrypts
// with the barrier's current epoch key. This must be called after key
// rotation to re-wrap secrets under the new DEK.
func (s *SealStore) Reseal(ctx context.Context, name string) error {
	if name == "" {
		return ErrInvalidSecretName
	}
	if s.barrier.IsSealed() {
		return &SealStoreError{Op: "reseal", Key: name, Err: ErrSealStoreBarrierSealed}
	}
	// Read the plaintext (barrier decrypts with current or old epoch key).
	plaintext, err := s.Get(ctx, name)
	if err != nil {
		return &SealStoreError{Op: "reseal", Key: name, Err: err}
	}
	// Delete old ciphertext.
	key := s.prefix + name
	if delErr := s.barrier.Delete(ctx, key); delErr != nil {
		return &SealStoreError{Op: "reseal", Key: name, Err: delErr}
	}
	// Re-encrypt with current epoch key.
	if putErr := s.barrier.Put(ctx, key, plaintext); putErr != nil {
		return &SealStoreError{Op: "reseal", Key: name, Err: putErr}
	}
	s.logger.Debug("seal store: secret resealed",
		slog.String("name", name))
	return nil
}

// SealStoreStatus contains the current state of a SealStore.
type SealStoreStatus struct {
	// Available is true when the barrier is unsealed and the store
	// can accept operations.
	Available bool `json:"available"`

	// Strategy identifies which sealing strategy protects the barrier.
	Strategy StrategyID `json:"strategy,omitempty"`

	// SecretCount is the number of secrets currently stored.
	SecretCount int `json:"secret_count"`

	// SecretNames lists all stored secret names.
	SecretNames []string `json:"secret_names,omitempty"`
}

// Status returns the current state of the seal store including
// barrier availability, active strategy, and secret count.
func (s *SealStore) Status(ctx context.Context) (*SealStoreStatus, error) {
	barrierStatus := s.barrier.Status()
	status := &SealStoreStatus{
		Available: !barrierStatus.Sealed,
		Strategy:  barrierStatus.Strategy,
	}
	if barrierStatus.Sealed {
		return status, nil
	}
	names, err := s.List(ctx)
	if err != nil {
		return nil, &SealStoreError{Op: "status", Err: err}
	}
	status.SecretCount = len(names)
	status.SecretNames = names
	return status, nil
}

// Close marks the seal store as closed.
func (s *SealStore) Close() error {
	s.closed.Store(true)
	return nil
}
