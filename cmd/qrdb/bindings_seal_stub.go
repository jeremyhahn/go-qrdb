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
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealBinding is a stub when embedded server support is not compiled
// in (built without the qrdb_embedded tag).
type SealBinding struct{}

// NewSealBinding creates a stub SealBinding.
func NewSealBinding(_ *sdk.ConcreteLogger, _ *EmbeddedServerBinding, _ *ConnectionBinding) *SealBinding {
	return &SealBinding{}
}

// SetConfigBinding is a no-op for the stub.
func (b *SealBinding) SetConfigBinding(_ *ConfigBinding) {}

// Initialize returns a FeatureDisabledError.
func (b *SealBinding) Initialize(_ string) error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// InitializeShamir returns a FeatureDisabledError.
func (b *SealBinding) InitializeShamir(_ string, _, _ int) (*BootstrapResult, error) {
	return nil, &FeatureDisabledError{Feature: "embedded"}
}

// Unseal returns a FeatureDisabledError.
func (b *SealBinding) Unseal(_ string) (*UnsealProgress, error) {
	return nil, &FeatureDisabledError{Feature: "embedded"}
}

// Seal returns a FeatureDisabledError.
func (b *SealBinding) Seal(_ string) error {
	return &FeatureDisabledError{Feature: "embedded"}
}

// ClusterUnseal returns a FeatureDisabledError.
func (b *SealBinding) ClusterUnseal() (*transport.ClusterSeal, error) {
	return nil, &FeatureDisabledError{Feature: "embedded"}
}

// Status returns a FeatureDisabledError.
func (b *SealBinding) Status() (*SealStatus, error) {
	return nil, &FeatureDisabledError{Feature: "embedded"}
}

// EnableEncryption returns a FeatureDisabledError.
func (b *SealBinding) EnableEncryption(_ *transport.BarrierEnableEncryptionRequest) (*EnableEncryptionResult, error) {
	return nil, &FeatureDisabledError{Feature: "embedded"}
}
