//go:build qrdb_sql

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

package pgcatalog

import (
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression/function"
)

// NewPgSleep returns PostgreSQL's pg_sleep(seconds), served by
// go-mysql-server's SLEEP implementation rather than a duplicate: it waits
// the given (fractional) number of seconds and ends early when the
// statement's context is cancelled -- the statement timeout included. It
// returns 0 where PostgreSQL returns void; a client reading pg_sleep's result
// is reading nothing it can use either way.
func NewPgSleep() sqle.Function {
	return sqle.Function1{Name: "pg_sleep", Fn: function.NewSleep}
}
