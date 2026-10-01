// Copyright (c) 2026 Jeremy Hahn
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

// Package audit provides the compliance audit trail: an AuditLogger sink
// interface, the sinks that implement it, and the emit helpers that decide
// what happens when a sink refuses.
//
// There is no configuration struct and no factory. A caller composes the
// sinks it wants and holds the result; NewAggregateLogger fans one Emit out
// to several. The node composes a ChainFileLogger over an append-only
// hash-chained NDJSON file, a RingBuffer for recent history, and a SlogSink
// for the operator's log.
//
// Query is served by whichever composed sink can answer it: ChainFileLogger
// scans its file, RingBuffer serves from memory, and the sinks that keep
// nothing (NoopLogger, SlogSink) report Total: -1 to say "not answerable
// here" rather than "no results". An aggregate returns the first non-empty
// page from its children.
//
// This comment previously described "configurable dual-backend audit logging
// with support for internal KVStore database and/or filesystem JSON log
// output". There has never been a KVStore backend, an `__sys:audit/` key
// space, or anything configurable about which backend is used (go-qrdb#446).
package audit

import "time"

const (
	// ResultSuccess indicates the audited action completed successfully.
	ResultSuccess = "success"

	// ResultDenied indicates the audited action was denied.
	ResultDenied = "denied"
)

// Entry represents a single audit log event.
type Entry struct {
	Timestamp time.Time         `json:"timestamp"`
	TenantID  string            `json:"tenantID"`
	Subject   string            `json:"subject"`
	Action    string            `json:"action"`
	Resource  string            `json:"resource"`
	Result    string            `json:"result"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}
