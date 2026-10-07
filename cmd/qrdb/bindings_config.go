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
	"sync/atomic"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// ConfigInitializedResponse contains the server initialization status.
type ConfigInitializedResponse struct {
	Initialized bool `json:"initialized"`
}

// ConfigBinding provides server-level configuration queries to the
// Wails frontend. It checks whether the server has completed first-run
// setup by examining saved embedded config and connection state.
type ConfigBinding struct {
	logger             *sdk.ConcreteLogger
	server             *EmbeddedServerBinding
	conn               *ConnectionBinding
	sessionInitialized atomic.Bool
}

// NewConfigBinding creates a new ConfigBinding.
func NewConfigBinding(log *sdk.ConcreteLogger, server *EmbeddedServerBinding, conn *ConnectionBinding) *ConfigBinding {
	return &ConfigBinding{
		logger: log,
		server: server,
		conn:   conn,
	}
}

// Initialized returns whether the server has completed first-run setup.
// Returns true when initialization was completed in the current session,
// when a saved embedded config exists on disk, or when a remote
// connection is already active.
func (b *ConfigBinding) Initialized() *ConfigInitializedResponse {
	b.logger.Debug("Initialized called")

	if b.sessionInitialized.Load() {
		b.logger.Debug("Initialized: session initialized")
		return &ConfigInitializedResponse{Initialized: true}
	}

	if b.server != nil && b.server.HasSavedConfig() {
		b.logger.Debug("Initialized: has saved embedded config")
		return &ConfigInitializedResponse{Initialized: true}
	}

	if b.conn != nil && b.conn.IsConnected() {
		b.logger.Debug("Initialized: remote connection active")
		return &ConfigInitializedResponse{Initialized: true}
	}

	b.logger.Debug("Initialized: not initialized")
	return &ConfigInitializedResponse{Initialized: false}
}

// MarkInitialized marks the server as initialized for the current
// session. Called by SealBinding after successful setup.
func (b *ConfigBinding) MarkInitialized() {
	b.logger.Debug("MarkInitialized called")
	b.sessionInitialized.Store(true)
}

// IsEmbeddedAvailable reports whether the binary was compiled with
// embedded server support.
func (b *ConfigBinding) IsEmbeddedAvailable() bool {
	return IsEmbeddedAvailable()
}
