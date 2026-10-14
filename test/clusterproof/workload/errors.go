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

//go:build clusterproof

package workload

import (
	"strconv"
	"strings"
)

// ConfigError is a configuration the workload cannot run.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return "workload: config " + e.Field + ": " + e.Reason
}

// UnknownProtocolError is a Config.Protocol with no SDK transport behind it.
type UnknownProtocolError struct {
	Protocol string
}

func (e *UnknownProtocolError) Error() string {
	return "workload: unknown protocol " + strconv.Quote(e.Protocol) + " (want quic, grpc or rest)"
}

// UnsupportedTransportError is a transport that connected but does not carry
// the guarded verbs the workload drives.
type UnsupportedTransportError struct {
	Protocol string
}

func (e *UnsupportedTransportError) Error() string {
	return "workload: transport " + strconv.Quote(e.Protocol) + " does not support compare-and-swap"
}

// DialError is a failure to reach one endpoint.
type DialError struct {
	Endpoint string
	Err      error
}

func (e *DialError) Error() string {
	return "workload: dial " + e.Endpoint + ": " + e.Err.Error()
}

func (e *DialError) Unwrap() error { return e.Err }

// FinalReadError lists the keys no endpoint answered during the final read.
// The history still records every attempt; the ledger reports those keys as
// unread.
type FinalReadError struct {
	Keys []string
}

func (e *FinalReadError) Error() string {
	return "workload: final read: no endpoint answered for " + strconv.Itoa(len(e.Keys)) +
		" key(s): " + strings.Join(e.Keys, ", ")
}
