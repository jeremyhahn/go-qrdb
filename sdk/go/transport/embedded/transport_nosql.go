//go:build !qrdb_sql

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

package embedded

// sqlCache is empty without the qrdb_sql build tag: there is no SQL query
// service to cache, and the transport does not implement
// transport.SQLProvider. transport_sql.go declares the real one.
type sqlCache struct{} //nolint:unused // only the Transport.sql field holds it
