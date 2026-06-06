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

package sdk

import qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"

// ---------------------------------------------------------------------------
// Embedded qrdb server options
//
// QuicBaaS composes an embedded qrdb server by supplying these functional
// options. Re-exported so the composer never imports pkg/server directly.
// ---------------------------------------------------------------------------

// ServerOption configures an embedded qrdb server at construction time.
type ServerOption = qrdbserver.ServerOption

// WithStrategyRegistry attaches a seal strategy registry to the embedded
// qrdb server.
var WithStrategyRegistry = qrdbserver.WithStrategyRegistry

// WithPINBackend attaches a PIN backend to the embedded qrdb server.
var WithPINBackend = qrdbserver.WithPINBackend

// WithFactorRegistry attaches an auth factor registry to the embedded qrdb
// server.
var WithFactorRegistry = qrdbserver.WithFactorRegistry
