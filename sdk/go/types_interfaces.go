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

import "github.com/jeremyhahn/go-qrdb/pkg/interfaces"

// ---------------------------------------------------------------------------
// Logger adapter
// ---------------------------------------------------------------------------

// Logger is the logging interface used throughout go-qrdb. Implementations
// adapt concrete loggers (slog, zerolog, etc.) to this interface.
type Logger = interfaces.Logger

// LogLevel identifies the severity of a log message.
type LogLevel = interfaces.LogLevel

// LogLevel constants.
const (
	LogLevelDebug = interfaces.LogLevelDebug
	LogLevelInfo  = interfaces.LogLevelInfo
	LogLevelWarn  = interfaces.LogLevelWarn
	LogLevelError = interfaces.LogLevelError
	LogLevelFatal = interfaces.LogLevelFatal
)

// ---------------------------------------------------------------------------
// HNSW vector index
// ---------------------------------------------------------------------------

// HNSWIndex is the interface for HNSW approximate nearest-neighbor indexes.
type HNSWIndex = interfaces.HNSWIndex

// HNSWSearchResult holds a single search result from an HNSW index.
type HNSWSearchResult = interfaces.HNSWSearchResult

// HNSWStats holds statistics about an HNSW index.
type HNSWStats = interfaces.HNSWStats

// HNSWIndexFactory creates HNSW index instances.
type HNSWIndexFactory = interfaces.HNSWIndexFactory

// HNSWConfig configures HNSW index parameters.
type HNSWConfig = interfaces.HNSWConfig

// DefaultHNSWConfig returns the default HNSW configuration.
var DefaultHNSWConfig = interfaces.DefaultHNSWConfig

// ---------------------------------------------------------------------------
// Raft node
// ---------------------------------------------------------------------------

// RaftNode is the interface for Raft consensus node operations.
type RaftNode = interfaces.RaftNode
