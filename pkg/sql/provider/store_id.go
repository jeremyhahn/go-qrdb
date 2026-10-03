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

package provider

// storeID names the (tenant, database) store a relational table's rows, its
// row locks and its planner estimates live in. It is a struct rather than a
// joined string so no tenant or database name can be read as part of the
// other; the one place it is rendered as a string is lockScope.
type storeID struct {
	tenant, db string
}

// lockScope is the scope the store's row-lock table is named by (see
// rowlock.New): "<tenant>/<database>". A transaction folds its lock release
// into its commit only when this matches the scope its lock owner is bound
// to, so both sides must render it here.
func (s storeID) lockScope() string {
	return s.tenant + "/" + s.db
}
