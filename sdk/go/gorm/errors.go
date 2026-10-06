//go:build qrdb_sql

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

package gorm

import "strconv"

// UnsupportedTransportError is returned by Open when the DSN names a
// transport GORM cannot run over.
//
// The REST and gRPC SQL API accepts a single statement per request with no
// field for bind parameters, no session affinity for transactions, and no
// typed result metadata; sdk/go/driver already refuses parameters and
// transactions on those schemes. GORM issues parameterized statements and
// wraps writes in transactions, so the DSN is refused here rather than opened
// and left to fail on the first query.
type UnsupportedTransportError struct {
	// Scheme is the DSN scheme that was refused ("rest" or "grpc").
	Scheme string
}

// Error returns a human-readable description naming the refused scheme and
// the alternatives that do work.
func (e *UnsupportedTransportError) Error() string {
	return "gorm: qrdb " + strconv.Quote(e.Scheme) + " transport does not support GORM " +
		"(no bind parameters, transactions, or typed results over the network SQL API); " +
		"use a node:// DSN, or gorm.io/driver/mysql over qrdb's MySQL wire port"
}
