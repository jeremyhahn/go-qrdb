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

package service

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// SQL table privileges — the data plane
//
// This is the persisted, Raft-replicated record of which identity may touch
// which table and which column inside ONE database. It is deliberately
// disjoint from the service RBAC store (pkg/auth/rbac), which is the control
// plane: RBAC decides whether an identity may reach a SQL endpoint at all and
// whether it may create or drop a database; these records decide what it may
// do to the objects inside one. Neither consults the other.
//
// # Activation latch
//
// A database with no privilege catalog is UNRESTRICTED: every identity may do
// everything the SQL surface allows. The first privilege mutation writes the
// latch (privilegeLatchKey) and from that moment the database is DEFAULT-DENY
// for every session: an identity may do only what its record grants. REVOKE
// never clears the latch, and nothing else deletes it, so the transition is
// monotonic and one-way.
//
// This is MySQL's own model, made per-database. MySQL's privilege system is
// inert until the `mysql` schema's grant tables are loaded — see
// mysql_db.MySQLDb.Enabled(), which go-mysql-server consults before running a
// single privilege check, and which flips exactly once when the grant tables
// are populated. qrdb applies the same idea at database granularity so that
// enabling privileges on one database cannot lock a caller out of another,
// and applies it monotonically so that the security posture of a database can
// only ever tighten.
//
// The system tenant is exempt permanently (see PrivilegeResolution.Latched's
// consumers): there is no `--skip-grant-tables` recovery mode in qrdb, so
// latching must never be able to lock the operator out of their own database.
// ---------------------------------------------------------------------------

// PrivilegeMask is a set of table privileges held as a bit field.
//
// It is a bit field rather than a slice of names because it is consulted on
// the query hot path — once per table, per column, per statement — where an
// AND is the whole check. It is persisted as its NAMES (see MarshalJSON), not
// as the integer, so a stored record stays readable and cannot be silently
// reinterpreted if the bit order ever changes.
type PrivilegeMask uint32

// The privileges qrdb's per-database data plane can represent and enforce.
//
// The set is deliberately limited to privileges that name an object inside a
// database. Server-wide privileges (SUPER, PROCESS, RELOAD, FILE, SHUTDOWN,
// REPLICATION *, CREATE USER, CREATE/DROP ROLE, ...) have no home here and are
// refused rather than stored: they are the service RBAC control plane's
// concern, and accepting a grant this layer would never enforce would be worse
// than refusing it.
const (
	// PrivilegeSelect permits reading rows.
	PrivilegeSelect PrivilegeMask = 1 << iota

	// PrivilegeInsert permits adding rows.
	PrivilegeInsert

	// PrivilegeUpdate permits modifying existing rows.
	PrivilegeUpdate

	// PrivilegeDelete permits removing rows.
	PrivilegeDelete

	// PrivilegeCreate permits creating tables.
	PrivilegeCreate

	// PrivilegeDrop permits dropping and truncating tables.
	PrivilegeDrop

	// PrivilegeAlter permits altering and renaming tables.
	PrivilegeAlter

	// PrivilegeIndex permits creating and dropping indexes.
	PrivilegeIndex

	// PrivilegeReferences permits declaring foreign keys against a table.
	PrivilegeReferences

	// PrivilegeGrantOption permits granting and revoking the privileges the
	// grantee itself holds. It is never included in ALL PRIVILEGES, matching
	// MySQL, where WITH GRANT OPTION is a separate clause.
	PrivilegeGrantOption
)

// PrivilegeAll is every privilege GRANT ALL PRIVILEGES confers. It excludes
// PrivilegeGrantOption for the reason given on that constant.
const PrivilegeAll = PrivilegeSelect | PrivilegeInsert | PrivilegeUpdate |
	PrivilegeDelete | PrivilegeCreate | PrivilegeDrop | PrivilegeAlter |
	PrivilegeIndex | PrivilegeReferences

// PrivilegeAllName is the canonical MySQL spelling of PrivilegeAll, accepted
// on input and emitted by SHOW GRANTS when the whole set is held.
const PrivilegeAllName = "ALL PRIVILEGES"

// privilegeOrder lists every single-bit privilege in the order MySQL prints
// them, which is also the order Names and String emit. It is the single
// declaration the name lookup tables below are derived from, so a privilege
// cannot be added to one and forgotten in the other.
var privilegeOrder = []struct {
	mask PrivilegeMask
	name string
}{
	{PrivilegeSelect, "SELECT"},
	{PrivilegeInsert, "INSERT"},
	{PrivilegeUpdate, "UPDATE"},
	{PrivilegeDelete, "DELETE"},
	{PrivilegeCreate, "CREATE"},
	{PrivilegeDrop, "DROP"},
	{PrivilegeAlter, "ALTER"},
	{PrivilegeIndex, "INDEX"},
	{PrivilegeReferences, "REFERENCES"},
	{PrivilegeGrantOption, "GRANT OPTION"},
}

// privilegeByName resolves a canonical MySQL privilege name to its bit.
var privilegeByName = func() map[string]PrivilegeMask {
	m := make(map[string]PrivilegeMask, len(privilegeOrder)+1)
	for _, p := range privilegeOrder {
		m[p.name] = p.mask
	}
	m[PrivilegeAllName] = PrivilegeAll
	return m
}()

// PrivilegeByName returns the bit for a canonical MySQL privilege name
// ("SELECT", "GRANT OPTION", "ALL PRIVILEGES", ...). The lookup is
// case-insensitive. ok is false for a name this layer cannot represent.
func PrivilegeByName(name string) (PrivilegeMask, bool) {
	m, ok := privilegeByName[strings.ToUpper(strings.TrimSpace(name))]
	return m, ok
}

// Has reports whether m holds every privilege in want. An empty want is
// always held, which is what makes a USAGE grant (no privileges) a no-op
// rather than a denial.
func (m PrivilegeMask) Has(want PrivilegeMask) bool {
	return m&want == want
}

// HasAny reports whether m holds at least one privilege in want.
func (m PrivilegeMask) HasAny(want PrivilegeMask) bool {
	return m&want != 0
}

// Names returns the canonical MySQL names of the privileges in m, in
// privilegeOrder. It never collapses the full set to ALL PRIVILEGES; String
// does that, and the stored form deliberately does not (see MarshalJSON).
func (m PrivilegeMask) Names() []string {
	names := make([]string, 0, len(privilegeOrder))
	for _, p := range privilegeOrder {
		if m&p.mask != 0 {
			names = append(names, p.name)
		}
	}
	return names
}

// String renders m the way SHOW GRANTS does: "ALL PRIVILEGES" when the whole
// grantable set is held, "USAGE" when none is, and the comma-separated names
// otherwise. GRANT OPTION is reported separately by the SHOW GRANTS renderer
// (MySQL prints it as a WITH clause), so it is excluded here.
func (m PrivilegeMask) String() string {
	grantable := m &^ PrivilegeGrantOption
	switch {
	case grantable == PrivilegeAll:
		return PrivilegeAllName
	case grantable == 0:
		return "USAGE"
	default:
		return strings.Join(grantable.Names(), ", ")
	}
}

// MarshalJSON encodes m as the array of its canonical privilege names.
//
// Names rather than the integer: a privilege record is a security decision
// that outlives the process that wrote it, and an operator reading the store
// must be able to see what it says. It also decouples the on-disk meaning
// from the bit assignments, which the integer form would freeze forever.
func (m PrivilegeMask) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Names())
}

// UnmarshalJSON decodes the array of privilege names MarshalJSON produced.
// An unrecognized name is an error rather than a silent drop: a record this
// build cannot fully understand must not be interpreted as a smaller set of
// privileges than it grants, nor as a larger one.
func (m *PrivilegeMask) UnmarshalJSON(data []byte) error {
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return err
	}
	var out PrivilegeMask
	for _, name := range names {
		bit, ok := PrivilegeByName(name)
		if !ok {
			return &PrivilegeUnknownNameError{Name: name}
		}
		out |= bit
	}
	*m = out
	return nil
}

// ---------------------------------------------------------------------------
// Stored record
// ---------------------------------------------------------------------------

// PrivilegeSet is one grantee's complete privilege record within one
// database: the database tier (GRANT ... ON db.*), the table tier
// (ON db.tbl), and the column tier (ON db.tbl(col)).
//
// There is no global (ON *.*) tier. The catalog, and the activation latch
// that arms it, are per-database by construction, so a server-wide grant has
// nowhere to live and no meaning here; PrivilegeService refuses it rather
// than narrowing it silently to one database. Server-wide authority is the
// service RBAC control plane's to grant.
type PrivilegeSet struct {
	// Grantee is the canonical grantee this record belongs to.
	Grantee string `json:"grantee"`

	// Database is the database this record is scoped to.
	Database string `json:"database"`

	// OnDatabase holds the privileges granted at the database tier, which
	// apply to every table in the database including tables created later.
	OnDatabase PrivilegeMask `json:"on_database,omitempty"`

	// OnTables maps table name to the privileges granted on that table.
	OnTables map[string]PrivilegeMask `json:"on_tables,omitempty"`

	// OnColumns maps table name to column name to the privileges granted on
	// that column.
	OnColumns map[string]map[string]PrivilegeMask `json:"on_columns,omitempty"`

	// UpdatedAt records the last mutation time in UTC.
	UpdatedAt time.Time `json:"updated_at"`
}

// newPrivilegeSet returns an empty record for the given grantee and database.
func newPrivilegeSet(database, grantee string) *PrivilegeSet {
	return &PrivilegeSet{Grantee: grantee, Database: database}
}

// IsEmpty reports whether the record grants nothing at any tier. An empty
// record is deleted rather than stored, so an absent record and an empty one
// mean the same thing.
func (s *PrivilegeSet) IsEmpty() bool {
	if s.OnDatabase != 0 {
		return false
	}
	for _, m := range s.OnTables {
		if m != 0 {
			return false
		}
	}
	for _, cols := range s.OnColumns {
		for _, m := range cols {
			if m != 0 {
				return false
			}
		}
	}
	return true
}

// Table returns the privileges that apply to table: the database tier plus
// anything granted on the table itself.
func (s *PrivilegeSet) Table(table string) PrivilegeMask {
	if s == nil {
		return 0
	}
	return s.OnDatabase | s.OnTables[table]
}

// Column returns the privileges that apply to one column of table: the
// database tier, the table tier, and anything granted on the column itself.
func (s *PrivilegeSet) Column(table, column string) PrivilegeMask {
	if s == nil {
		return 0
	}
	m := s.Table(table)
	if cols, ok := s.OnColumns[table]; ok {
		m |= cols[column]
	}
	return m
}

// AnyOnTable returns every privilege held anywhere at or below table: the
// database tier, the table tier, and the union of its column tiers. It
// answers "is this table reachable at all", which is the check MySQL makes
// before reporting a table as inaccessible rather than as lacking one
// specific privilege.
func (s *PrivilegeSet) AnyOnTable(table string) PrivilegeMask {
	if s == nil {
		return 0
	}
	m := s.Table(table)
	for _, colMask := range s.OnColumns[table] {
		m |= colMask
	}
	return m
}

// Any returns every privilege the record holds at any tier, which answers
// "is this database reachable at all".
func (s *PrivilegeSet) Any() PrivilegeMask {
	if s == nil {
		return 0
	}
	m := s.OnDatabase
	for _, tblMask := range s.OnTables {
		m |= tblMask
	}
	for _, cols := range s.OnColumns {
		for _, colMask := range cols {
			m |= colMask
		}
	}
	return m
}

// Tables returns the names of every table named at the table or column tier,
// sorted, so that SHOW GRANTS renders deterministically.
func (s *PrivilegeSet) Tables() []string {
	if s == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(s.OnTables)+len(s.OnColumns))
	for name := range s.OnTables {
		seen[name] = struct{}{}
	}
	for name := range s.OnColumns {
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Columns returns the columns of table named at the column tier, sorted.
func (s *PrivilegeSet) Columns(table string) []string {
	if s == nil {
		return nil
	}
	cols := s.OnColumns[table]
	names := make([]string, 0, len(cols))
	for name := range cols {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// grant adds the privileges in g to the record, creating the table and
// column tiers on demand.
func (s *PrivilegeSet) grant(g PrivilegeGrant) {
	switch {
	case g.Table == "":
		s.OnDatabase |= g.Privileges
	case g.Column == "":
		if s.OnTables == nil {
			s.OnTables = make(map[string]PrivilegeMask, 1)
		}
		s.OnTables[g.Table] |= g.Privileges
	default:
		if s.OnColumns == nil {
			s.OnColumns = make(map[string]map[string]PrivilegeMask, 1)
		}
		cols := s.OnColumns[g.Table]
		if cols == nil {
			cols = make(map[string]PrivilegeMask, 1)
			s.OnColumns[g.Table] = cols
		}
		cols[g.Column] |= g.Privileges
	}
}

// revoke removes the privileges in g from the record, pruning tiers that
// become empty so that a fully revoked record compares equal to an absent
// one (see IsEmpty).
func (s *PrivilegeSet) revoke(g PrivilegeGrant) {
	switch {
	case g.Table == "":
		s.OnDatabase &^= g.Privileges
	case g.Column == "":
		// Revoking at the table tier also clears the column tiers beneath
		// it, matching MySQL: REVOKE SELECT ON db.t leaves no column-level
		// SELECT behind for the same table.
		if s.OnTables != nil {
			s.OnTables[g.Table] &^= g.Privileges
			if s.OnTables[g.Table] == 0 {
				delete(s.OnTables, g.Table)
			}
		}
		s.revokeColumns(g.Table, "", g.Privileges)
	default:
		s.revokeColumns(g.Table, g.Column, g.Privileges)
	}
}

// revokeColumns clears privs from one column of table, or from every column
// of table when column is empty, pruning entries that become empty.
func (s *PrivilegeSet) revokeColumns(table, column string, privs PrivilegeMask) {
	cols, ok := s.OnColumns[table]
	if !ok {
		return
	}
	for name := range cols {
		if column != "" && name != column {
			continue
		}
		cols[name] &^= privs
		if cols[name] == 0 {
			delete(cols, name)
		}
	}
	if len(cols) == 0 {
		delete(s.OnColumns, table)
	}
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// PrivilegeGrant is one privilege assignment at exactly one tier. Table
// empty means the database tier; Table set with Column empty means the table
// tier; both set means the column tier.
//
// A statement that names several tiers at once — GRANT SELECT (id), INSERT
// ON db.t — is flattened into one PrivilegeGrant per tier by the caller, so
// that this layer has exactly one shape to apply.
type PrivilegeGrant struct {
	Table      string        `json:"table,omitempty"`
	Column     string        `json:"column,omitempty"`
	Privileges PrivilegeMask `json:"privileges"`
}

// GrantPrivilegesRequest adds privileges to a grantee's record, arming the
// database's activation latch if it is not already armed.
type GrantPrivilegesRequest struct {
	Database string           `json:"database"`
	Grantee  string           `json:"grantee"`
	Grants   []PrivilegeGrant `json:"grants"`
}

// RevokePrivilegesRequest removes privileges from a grantee's record. It
// never clears the activation latch.
type RevokePrivilegesRequest struct {
	Database string           `json:"database"`
	Grantee  string           `json:"grantee"`
	Revokes  []PrivilegeGrant `json:"revokes"`
}

// ResolvePrivilegesRequest asks for the authorization state of one
// (database, grantee) pair, telling the service what the caller already
// holds so that it can answer with the fewest reads.
//
// KnownGeneration is the presence signal: zero means the caller holds no
// snapshot at all and wants the full answer. It is never zero for a real
// snapshot because the latch counter starts at one.
type ResolvePrivilegesRequest struct {
	Database        string `json:"database"`
	Grantee         string `json:"grantee"`
	KnownGeneration uint64 `json:"known_generation,omitempty"`
	KnownEpoch      uint64 `json:"known_epoch,omitempty"`
}

// PrivilegeResolution is the authorization state of one (database, grantee)
// pair at a point in time.
type PrivilegeResolution struct {
	// Database and Grantee echo the request.
	Database string `json:"database"`
	Grantee  string `json:"grantee"`

	// Latched reports whether the database has a privilege catalog. False
	// means the database is unrestricted and the remaining fields are zero.
	Latched bool `json:"latched"`

	// Generation is the database's privilege-catalog generation, bumped by
	// every mutation to any grantee. It is the gate a caller's snapshot
	// cache validates against.
	Generation uint64 `json:"generation,omitempty"`

	// Epoch is the grantee's own record generation, bumped only by
	// mutations to that grantee. Zero means the grantee has no record.
	Epoch uint64 `json:"epoch,omitempty"`

	// Set is the grantee's privilege record, nil when Unchanged is true or
	// when the grantee has no record.
	Set *PrivilegeSet `json:"set,omitempty"`

	// Unchanged reports that the caller's cached Set is still current, so
	// Set was not read and is nil.
	Unchanged bool `json:"unchanged,omitempty"`
}

// ---------------------------------------------------------------------------
// Key layout
//
// Everything lives under "__sys:", which the internal-key guard
// (internal_keys.go) already excludes from user enumeration, and is scoped to
// the caller's (tenant, database) namespace by service.Registry exactly as
// the relational table catalog is.
// ---------------------------------------------------------------------------

const (
	// privilegeCatalogPrefix is the key space holding privilege records.
	privilegeCatalogPrefix = "__sys:privs/"

	// privilegeLatchSuffix names the per-database activation latch, whose
	// presence arms default-deny and whose value is the database's
	// monotonic catalog generation.
	privilegeLatchSuffix = "__latch"

	// privilegeEpochSuffix names a grantee's own record generation.
	privilegeEpochSuffix = "/__epoch"
)

// privilegeScanPrefix returns the prefix enumerating a database's privilege
// catalog.
func privilegeScanPrefix(database string) string {
	return privilegeCatalogPrefix + database + "/"
}

// privilegeLatchKey returns the key holding a database's activation latch
// and catalog generation.
func privilegeLatchKey(database string) string {
	return privilegeScanPrefix(database) + privilegeLatchSuffix
}

// privilegeGranteeKey returns the key holding one grantee's record.
func privilegeGranteeKey(database, grantee string) string {
	return privilegeScanPrefix(database) + grantee
}

// privilegeEpochKey returns the key holding one grantee's record generation.
func privilegeEpochKey(database, grantee string) string {
	return privilegeGranteeKey(database, grantee) + privilegeEpochSuffix
}
