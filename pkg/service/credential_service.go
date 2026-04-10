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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
)

// CredentialServicer provides credential management operations including
// storage, retrieval, submission, and deletion of named credentials.
type CredentialServicer interface {
	// CredentialStore persists a credential under the given name.
	CredentialStore(ctx context.Context, name string, value []byte) error

	// CredentialRetrieve returns the credential value for the given name.
	// For the manual strategy, this blocks until the credential is submitted
	// or the context is cancelled.
	CredentialRetrieve(ctx context.Context, name string) ([]byte, error)

	// CredentialSubmit submits a credential for the manual strategy,
	// notifying any blocked retrievers. For the barrier strategy, this
	// delegates to CredentialStore.
	CredentialSubmit(ctx context.Context, name string, value []byte) error

	// CredentialDelete removes the credential with the given name.
	CredentialDelete(ctx context.Context, name string) error

	// CredentialStrategy returns the active credential strategy name.
	CredentialStrategy() string
}

// CredentialService implements CredentialServicer by delegating to a
// credentials.CredentialService instance.
type CredentialService struct {
	creds *credentials.CredentialService
}

// Compile-time interface check.
var _ CredentialServicer = (*CredentialService)(nil)

// NewCredentialService creates a new CredentialServicer backed by the
// given credentials.CredentialService.
func NewCredentialService(c *credentials.CredentialService) CredentialServicer {
	return &CredentialService{creds: c}
}

// CredentialStore persists a credential under the given name by delegating
// to the underlying CredentialService's StoreCredential method.
func (s *CredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	return s.creds.StoreCredential(ctx, name, value)
}

// CredentialRetrieve returns the credential value for the given name by
// delegating to the underlying CredentialService's RetrieveCredential method.
func (s *CredentialService) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) {
	return s.creds.RetrieveCredential(ctx, name)
}

// CredentialSubmit submits a credential by delegating to the underlying
// CredentialService's SubmitCredential method.
func (s *CredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	return s.creds.SubmitCredential(ctx, name, value)
}

// CredentialDelete removes a credential by delegating to the underlying
// CredentialService's DeleteCredential method.
func (s *CredentialService) CredentialDelete(ctx context.Context, name string) error {
	return s.creds.DeleteCredential(ctx, name)
}

// CredentialStrategy returns the active credential strategy name by
// delegating to the underlying CredentialService's Strategy method.
func (s *CredentialService) CredentialStrategy() string {
	return s.creds.Strategy()
}
