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

package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubClusterCAProvider is a minimal cakey.Provider double.
type stubClusterCAProvider struct {
	certErr error
}

func (s *stubClusterCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }

func (s *stubClusterCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	if s.certErr != nil {
		return nil, s.certErr
	}
	return &x509.Certificate{}, nil
}

func TestConfirmClusterCAPersisted_DeletesLocalKeyOnSuccess(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(keyPath, []byte("fake key material"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, true); err != nil {
		t.Fatalf("confirmClusterCAPersisted: %v", err)
	}

	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("ca-key.pem still exists after confirmed persistence (stat err = %v)", err)
	}
}

func TestConfirmClusterCAPersisted_NoLocalKeyToDelete(t *testing.T) {
	dir := t.TempDir()
	// hadLocalKey=false: nothing to delete, no file present either. Must
	// not error just because there is nothing to remove.
	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, false); err != nil {
		t.Fatalf("confirmClusterCAPersisted: %v", err)
	}
}

func TestConfirmClusterCAPersisted_ProviderFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(keyPath, []byte("fake key material"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	boom := errors.New("store unreachable")
	err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{certErr: boom}, dir, true)
	var persistErr *ClusterCAPersistError
	if !errors.As(err, &persistErr) {
		t.Fatalf("confirmClusterCAPersisted = %v, want *ClusterCAPersistError", err)
	}

	// The local key must NOT be deleted when persistence could not be
	// confirmed -- it is the only remaining copy of the key.
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("ca-key.pem was removed despite a failed persistence check: %v", err)
	}
}

func TestConfirmClusterCAPersisted_DeletionFailureIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	// hadLocalKey=true but no file present (e.g. removed out-of-band
	// between boots): deletion sees ErrNotExist and must not surface it.
	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, true); err != nil {
		t.Fatalf("confirmClusterCAPersisted: %v", err)
	}
}
