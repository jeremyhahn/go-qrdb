//go:build !qrdb_embedded

// Copyright 2026 Jeremy Hahn
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

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// EmbeddedServerBinding is a stub when embedded server support is not
// compiled in (built without the qrdb_embedded tag).
type EmbeddedServerBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewEmbeddedServerBinding creates a stub EmbeddedServerBinding.
func NewEmbeddedServerBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding, _ *SQLBinding) *EmbeddedServerBinding {
	return &EmbeddedServerBinding{logger: log, conn: conn}
}

// startup is a no-op for the stub.
func (b *EmbeddedServerBinding) startup(_ context.Context) {}

// shutdown is a no-op for the stub.
func (b *EmbeddedServerBinding) shutdown(_ context.Context) {}

// IsEmbedded always returns false when embedded is not compiled in.
func (b *EmbeddedServerBinding) IsEmbedded() bool { return false }

// HasSavedConfig always returns false when embedded is not compiled in.
func (b *EmbeddedServerBinding) HasSavedConfig() bool { return false }

// StartEmbedded returns a FeatureDisabledError.
func (b *EmbeddedServerBinding) StartEmbedded(_ *EmbeddedConfig) error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// StopEmbedded returns a FeatureDisabledError.
func (b *EmbeddedServerBinding) StopEmbedded() error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// RestoreEmbedded returns a FeatureDisabledError.
func (b *EmbeddedServerBinding) RestoreEmbedded() error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// ClearSavedConfig returns a FeatureDisabledError.
func (b *EmbeddedServerBinding) ClearSavedConfig() error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// Registry returns nil when embedded is not compiled in.
func (b *EmbeddedServerBinding) Registry() *DatabaseInstanceRegistry { return nil }

// DatabaseInstanceRegistry is a stub when embedded server support is
// not compiled in.
type DatabaseInstanceRegistry struct{}

// NewDatabaseInstanceRegistry creates a stub registry.
func NewDatabaseInstanceRegistry(_ *sdk.ConcreteLogger) *DatabaseInstanceRegistry {
	return &DatabaseInstanceRegistry{}
}

// Register returns a FeatureDisabledError.
func (r *DatabaseInstanceRegistry) Register(_ string, _ any) error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// Stop returns a FeatureDisabledError.
func (r *DatabaseInstanceRegistry) Stop(_ string) error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// StopAll returns a FeatureDisabledError.
func (r *DatabaseInstanceRegistry) StopAll() error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// Count returns zero when embedded is not compiled in.
func (r *DatabaseInstanceRegistry) Count() int64 { return 0 }

// Names returns nil when embedded is not compiled in.
func (r *DatabaseInstanceRegistry) Names() []string { return nil }
