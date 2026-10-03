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

// PostgreSQL spells random-UUID generation two ways: gen_random_uuid()
// (built in since PostgreSQL 13, pgcrypto before that) and
// uuid_generate_v4() (the uuid-ossp extension, which
// gorm.io/driver/postgres installs with CREATE EXTENSION before creating
// any table whose primary key defaults to a generated uuid). Both produce
// a version-4 UUID, which is exactly what go-mysql-server's own UUID()
// implementation emits, so both names are served by that implementation
// rather than a duplicate. The CREATE EXTENSION statement itself is a
// no-op on the wire -- see pkg/sql/pgsql's noopPrefixes.

// NewGenRandomUUID returns PostgreSQL's gen_random_uuid().
func NewGenRandomUUID() sqle.Function {
	return sqle.Function0{Name: "gen_random_uuid", Fn: function.NewUUIDFunc}
}

// NewUUIDGenerateV4 returns uuid-ossp's uuid_generate_v4().
func NewUUIDGenerateV4() sqle.Function {
	return sqle.Function0{Name: "uuid_generate_v4", Fn: function.NewUUIDFunc}
}
