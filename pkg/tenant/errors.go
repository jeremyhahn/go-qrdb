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

package tenant

import "errors"

var (
	// ErrTenantNotFound is returned when a requested tenant does not exist
	// in the metadata store.
	ErrTenantNotFound = errors.New("tenant: not found")

	// ErrTenantAlreadyExists is returned when attempting to create a tenant
	// that already has metadata stored.
	ErrTenantAlreadyExists = errors.New("tenant: already exists")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided.
	ErrEmptyTenantID = errors.New("tenant: empty tenant ID")

	// ErrEmptyTenantName is returned when an empty tenant name is provided.
	ErrEmptyTenantName = errors.New("tenant: empty tenant name")

	// ErrNilStore is returned when a nil storage backend is provided
	// to the tenant service constructor.
	ErrNilStore = errors.New("tenant: nil storage backend")

	// ErrNilRegistry is returned when a nil barrier registry is provided
	// to the tenant service constructor.
	ErrNilRegistry = errors.New("tenant: nil barrier registry")

	// ErrNilQuotas is returned when a nil quotas pointer is provided
	// to UpdateQuotas.
	ErrNilQuotas = errors.New("tenant: nil quotas")
)

// InvalidTenantIDError is returned when a tenant ID is empty or malformed.
type InvalidTenantIDError struct {
	TenantID string
	Reason   string
}

func (e *InvalidTenantIDError) Error() string {
	return "invalid tenant ID " + e.TenantID + ": " + e.Reason
}

// StoreError is returned when a storage operation fails within the
// tenant service.
type StoreError struct {
	Op  string
	Err error
}

func (e *StoreError) Error() string {
	return "tenant: " + e.Op + ": " + e.Err.Error()
}

func (e *StoreError) Unwrap() error {
	return e.Err
}

// BarrierError is returned when a barrier registry operation fails
// within the tenant service.
type BarrierError struct {
	TenantID string
	Op       string
	Err      error
}

func (e *BarrierError) Error() string {
	return "tenant: barrier " + e.Op + " for " + e.TenantID + ": " + e.Err.Error()
}

func (e *BarrierError) Unwrap() error {
	return e.Err
}
