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

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// --- a minimal, fully offline database/sql driver, used only to obtain a
// *sql.DB (which implements gorm.ConnPool for free) without opening any
// network connection or spawning a real qrdb engine. ---

const fakeDriverName = "qrdbgorm-fake-test-driver"

func init() {
	sql.Register(fakeDriverName, fakeDriverImpl{})
}

// fakeDriverImpl is a driver.Driver whose only behavior is answering
// "SELECT VERSION()" with a canned string; every other statement fails.
type fakeDriverImpl struct{}

func (fakeDriverImpl) Open(name string) (driver.Conn, error) {
	if name == "fail-to-open" {
		return nil, errors.New("fakeDriverImpl: refusing to open")
	}
	return &fakeConn{}, nil
}

type fakeConn struct{}

func (fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: Prepare not supported")
}
func (fakeConn) Close() error              { return nil }
func (fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fakeConn: Begin not supported") }

// QueryContext implements driver.QueryerContext so database/sql issues
// QueryRowContext without going through Prepare.
func (fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return &fakeVersionRows{}, nil
}

type fakeVersionRows struct {
	done bool
}

func (r *fakeVersionRows) Columns() []string { return []string{"VERSION()"} }
func (r *fakeVersionRows) Close() error      { return nil }
func (r *fakeVersionRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = "8.0.11-qrdb-fake"
	return nil
}

// openFakeConnPool returns a *sql.DB backed by fakeDriverImpl, usable
// directly as Config.Conn.
func openFakeConnPool(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := sql.Open(fakeDriverName, "fake")
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", fakeDriverName, err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// --- Name ---

func TestName_DefaultsToMySQL(t *testing.T) {
	dialector := New(Config{})
	if got := dialector.Name(); got != "mysql" {
		t.Errorf("Name() = %q, want %q", got, "mysql")
	}
}

func TestName_OverriddenByDialectName(t *testing.T) {
	dialector := New(Config{DialectName: "qrdb-custom"})
	if got := dialector.Name(); got != "qrdb-custom" {
		t.Errorf("Name() = %q, want %q", got, "qrdb-custom")
	}
}

func TestName_ZeroValueDialectorFallsBackToMySQL(t *testing.T) {
	var dialector Dialector
	if got := dialector.Name(); got != DefaultDialectName {
		t.Errorf("Name() = %q, want %q", got, DefaultDialectName)
	}
}

// --- Open / New defaults ---

func TestNew_FillsDocumentedDefaults(t *testing.T) {
	dialector := New(Config{}).(Dialector)

	if dialector.DriverName != DefaultDriverName {
		t.Errorf("DriverName = %q, want %q", dialector.DriverName, DefaultDriverName)
	}
	if dialector.DialectName != DefaultDialectName {
		t.Errorf("DialectName = %q, want %q", dialector.DialectName, DefaultDialectName)
	}
	if dialector.DefaultStringSize != defaultStringSize {
		t.Errorf("DefaultStringSize = %d, want %d", dialector.DefaultStringSize, defaultStringSize)
	}
	if dialector.DefaultDatetimePrecision == nil || *dialector.DefaultDatetimePrecision != defaultDatetimePrecision {
		t.Errorf("DefaultDatetimePrecision = %v, want pointer to %d", dialector.DefaultDatetimePrecision, defaultDatetimePrecision)
	}
}

func TestNew_PreservesCallerSuppliedValues(t *testing.T) {
	precision := 6
	dialector := New(Config{
		DriverName:                "custom-driver",
		DialectName:               "custom-dialect",
		DefaultStringSize:         64,
		DefaultDatetimePrecision:  &precision,
		DontSupportForShareClause: false,
	}).(Dialector)

	if dialector.DriverName != "custom-driver" {
		t.Errorf("DriverName = %q, want %q", dialector.DriverName, "custom-driver")
	}
	if dialector.DialectName != "custom-dialect" {
		t.Errorf("DialectName = %q, want %q", dialector.DialectName, "custom-dialect")
	}
	if dialector.DefaultStringSize != 64 {
		t.Errorf("DefaultStringSize = %d, want %d", dialector.DefaultStringSize, 64)
	}
	if dialector.DefaultDatetimePrecision != &precision {
		t.Error("DefaultDatetimePrecision was replaced instead of preserved")
	}
}

func TestOpen_SetsDSN(t *testing.T) {
	dialector := Open("embedded://memory?database=x").(Dialector)
	if dialector.DSN != "embedded://memory?database=x" {
		t.Errorf("DSN = %q, want %q", dialector.DSN, "embedded://memory?database=x")
	}
}

// --- Apply ---

func TestApply_InstallsRoundingNowFunc(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	cfg := &gorm.Config{}

	if err := dialector.Apply(cfg); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if cfg.NowFunc == nil {
		t.Fatal("Apply() did not install a NowFunc")
	}

	got := cfg.NowFunc()
	unit := time.Duration(1)
	for i := 0; i < 9-defaultDatetimePrecision; i++ {
		unit *= 10
	}
	if got.Nanosecond()%int(unit) != 0 {
		t.Errorf("NowFunc() = %v, want rounded to %d fractional digits", got, defaultDatetimePrecision)
	}
}

// TestApply_NowFuncMatchesColumnPrecision pins the invariant the rounding
// exists for: the NowFunc must not produce a timestamp finer than the
// DATETIME column defaultDatetimePrecision declares, or a value written and
// read back compares unequal on the digits the column truncated.
func TestApply_NowFuncMatchesColumnPrecision(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	cfg := &gorm.Config{}
	if err := dialector.Apply(cfg); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	field := parseField(t, &struct{ F time.Time }{})
	if got, want := dialector.DataTypeOf(field), "datetime(6) NULL"; got != want {
		t.Fatalf("DataTypeOf() = %q, want %q", got, want)
	}
	if field.Precision != defaultDatetimePrecision {
		t.Errorf("field.Precision = %d, want %d", field.Precision, defaultDatetimePrecision)
	}
}

func TestApply_DoesNotOverrideExistingNowFunc(t *testing.T) {
	sentinel := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := &gorm.Config{NowFunc: func() time.Time { return sentinel }}

	dialector := New(Config{}).(Dialector)
	if err := dialector.Apply(cfg); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := cfg.NowFunc(); !got.Equal(sentinel) {
		t.Errorf("NowFunc() = %v, want unchanged sentinel %v", got, sentinel)
	}
}

// --- Initialize ---

func TestInitialize_RejectsRESTTransport(t *testing.T) {
	_, err := gorm.Open(Open("rest://localhost:63001/mydb"), &gorm.Config{})

	var target *UnsupportedTransportError
	if !errors.As(err, &target) {
		t.Fatalf("err = %v (%T), want *UnsupportedTransportError", err, err)
	}
	if target.Scheme != "rest" {
		t.Errorf("Scheme = %q, want %q", target.Scheme, "rest")
	}
}

func TestInitialize_RejectsGRPCTransport(t *testing.T) {
	_, err := gorm.Open(Open("grpc://localhost:63000/mydb"), &gorm.Config{})

	var target *UnsupportedTransportError
	if !errors.As(err, &target) {
		t.Fatalf("err = %v (%T), want *UnsupportedTransportError", err, err)
	}
	if target.Scheme != "grpc" {
		t.Errorf("Scheme = %q, want %q", target.Scheme, "grpc")
	}
}

func TestInitialize_RejectsMalformedDSN(t *testing.T) {
	_, err := gorm.Open(Open(""), &gorm.Config{})
	if err == nil {
		t.Fatal("expected an error for an empty DSN")
	}
}

func TestInitialize_OpensEmbeddedDSNThroughSQLOpen(t *testing.T) {
	// Exercises the sql.Open(driverName, DSN) branch and its default
	// DriverName, using qrdb's own embedded:// transport, which is
	// in-process and offline (no network, no external server).
	db, err := gorm.Open(New(Config{DSN: "embedded://memory?database=initializetest"}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB() error = %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

func TestInitialize_UsesSuppliedConnDirectly(t *testing.T) {
	pool := openFakeConnPool(t)

	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: true})
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	got, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB() error = %v", err)
	}
	if got != pool {
		t.Error("Initialize did not use the supplied Conn directly")
	}
}

func TestInitialize_QueriesVersionUnlessSkipped(t *testing.T) {
	pool := openFakeConnPool(t)

	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: false})
	if _, err := gorm.Open(dialector, &gorm.Config{}); err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
}

func TestInitialize_VersionQueryFailureFailsInitialize(t *testing.T) {
	pool, err := sql.Open(fakeDriverName, "fail-to-open")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: false})
	if _, err := gorm.Open(dialector, &gorm.Config{}); err == nil {
		t.Fatal("expected Initialize to fail when the version probe fails")
	}
}

func TestInitialize_RegistersDefaultClauseBuildersWithoutOverwritingExisting(t *testing.T) {
	sentinelCalled := false
	sentinel := clause.ClauseBuilder(func(c clause.Clause, b clause.Builder) { sentinelCalled = true })

	pool := openFakeConnPool(t)
	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: true})

	db, err := gorm.Open(dialector, &gorm.Config{
		ClauseBuilders: map[string]clause.ClauseBuilder{ClauseOnConflict: sentinel},
	})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	got, ok := db.ClauseBuilders[ClauseOnConflict]
	if !ok {
		t.Fatal("ClauseOnConflict builder is missing")
	}
	got(clause.Clause{}, nil) //nolint:errcheck // the sentinel ignores its arguments
	if !sentinelCalled {
		t.Error("Initialize overwrote the pre-existing ON CONFLICT builder")
	}

	if _, ok := db.ClauseBuilders[ClauseValues]; !ok {
		t.Error("Initialize did not register the VALUES builder")
	}
}

// --- BindVarTo ---

func TestBindVarTo_WritesQuestionMark(t *testing.T) {
	dialector := New(Config{})
	stmt := newTestStatement(dialector.(Dialector))

	dialector.BindVarTo(stmt, stmt, "ignored")
	if got := stmt.SQL.String(); got != "?" {
		t.Errorf("BindVarTo wrote %q, want %q", got, "?")
	}
}

// --- QuoteTo ---

func TestQuoteTo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Every case below is checked against go-gorm/mysql's own QuoteTo
		// output for the identical input, since this implementation is a
		// verbatim copy of its state machine and go-gorm/mysql's own test
		// suite is the ground truth for what "correct" means here,
		// including for inputs (an empty string, a leading or trailing
		// dot) that are not realistic SQL identifiers.
		{"bare identifier", "x", "`x`"},
		{"empty string", "", "`"},
		{"dotted table.column", "a.b", "`a`.`b`"},
		{"three-part path", "a.b.c", "`a`.`b`.`c`"},
		{"already backtick-quoted", "`a`", "`a`"},
		{"quoted table, bare column", "`a`.b", "`a`.`b`"},
		{"bare table, quoted column", "a.`b`", "`a`.`b`"},
		{"quoted path", "`a`.`b`", "`a`.`b`"},
		{"embedded backtick", "a`b", "`a``b`"},
		{"embedded backtick after dot", "a.b`c", "`a`.`b``c`"},
		{"unicode identifier", "日本語", "`日本語`"},
		{"unicode with dot", "テーブル.カラム", "`テーブル`.`カラム`"},
		{"trailing dot", "a.", "`a`.`"},
		{"leading dot", ".a", "`.`a`"},
		{"double embedded backtick", "a``b", "`a``b`"},
		{"underscore and digits", "col_1", "`col_1`"},
		{"quoted table with embedded backtick", "`a``b`.c", "`a``b`.`c`"},
		{"single backtick only", "`", "```"},
		{"two backticks only", "``", "```"},
		{"mixed case", "MyTable", "`MyTable`"},
		{"database.table with backtick in second segment", "datadase.table`User", "`datadase`.`table``User`"},
		{"quoted table, trailing backtick on column", "`a`.b`", "`a`.`b```"},
		{"quoted table, embedded backtick then plain in column", "`a`.b`c", "`a`.`b``c`"},
		{"quoted table, quoted-then-trailing column", "`a`.`b`c`", "`a`.`b``c`"},
		{"triple backtick inside quotes", "`a```b`", "`a````b`"},
		{"whole identifier already quoted with dot inside", "`a.b`", "`a.b`"},
	}

	dialector := New(Config{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb clauseStringBuilder
			dialector.QuoteTo(&sb, tt.in)
			if got := sb.String(); got != tt.want {
				t.Errorf("QuoteTo(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- DefaultValueOf ---

func TestDefaultValueOf(t *testing.T) {
	dialector := New(Config{})
	got := dialector.DefaultValueOf(nil)

	expr, ok := got.(clause.Expr)
	if !ok {
		t.Fatalf("DefaultValueOf() returned %T, want clause.Expr", got)
	}
	if expr.SQL != "DEFAULT" {
		t.Errorf("DefaultValueOf().SQL = %q, want %q", expr.SQL, "DEFAULT")
	}
}

// --- Explain ---

func TestExplain(t *testing.T) {
	dialector := New(Config{})
	got := dialector.Explain("SELECT * FROM users WHERE name = ?", "bob")

	want := "SELECT * FROM users WHERE name = 'bob'"
	if got != want {
		t.Errorf("Explain() = %q, want %q", got, want)
	}
}

// --- SavePoint / RollbackTo ---

func TestSavePoint_ValidName(t *testing.T) {
	pool := openFakeConnPool(t)
	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: true})
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	// fakeConn refuses Prepare/Begin, so the SAVEPOINT statement itself
	// cannot succeed; this only exercises name validation passing through
	// to tx.Exec, not a real savepoint.
	err = dialector.(gorm.SavePointerDialectorInterface).SavePoint(db, "sp1")
	var invalid *InvalidSavePointNameError
	if errors.As(err, &invalid) {
		t.Errorf("SavePoint() rejected a valid name %q", "sp1")
	}
}

func TestSavePoint_InvalidName(t *testing.T) {
	pool := openFakeConnPool(t)
	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: true})
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	tests := []string{"", "1sp", "sp-1", "sp 1", "sp;DROP TABLE x"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			err := dialector.(gorm.SavePointerDialectorInterface).SavePoint(db, name)
			var invalid *InvalidSavePointNameError
			if !errors.As(err, &invalid) {
				t.Fatalf("SavePoint(%q) error = %v (%T), want *InvalidSavePointNameError", name, err, err)
			}
			if invalid.Name != name {
				t.Errorf("Name = %q, want %q", invalid.Name, name)
			}
		})
	}
}

func TestRollbackTo_InvalidName(t *testing.T) {
	pool := openFakeConnPool(t)
	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: true})
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	err = dialector.(gorm.SavePointerDialectorInterface).RollbackTo(db, "bad name")
	var invalid *InvalidSavePointNameError
	if !errors.As(err, &invalid) {
		t.Fatalf("RollbackTo() error = %v (%T), want *InvalidSavePointNameError", err, err)
	}
}

func TestRollbackTo_ValidName(t *testing.T) {
	pool := openFakeConnPool(t)
	dialector := New(Config{Conn: pool, SkipInitializeWithVersion: true})
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}

	// fakeConn refuses Prepare/Begin, so the ROLLBACK TO SAVEPOINT statement
	// itself cannot succeed; this only exercises name validation passing
	// through to tx.Exec, not a real rollback.
	err = dialector.(gorm.SavePointerDialectorInterface).RollbackTo(db, "sp1")
	var invalid *InvalidSavePointNameError
	if errors.As(err, &invalid) {
		t.Errorf("RollbackTo() rejected a valid name %q", "sp1")
	}
}

// --- compile-time interface assertions, exercised at test-build time ---

func TestDialector_ImplementsGormInterfaces(t *testing.T) {
	var (
		_ gorm.Dialector                     = Dialector{}
		_ gorm.ErrorTranslator               = Dialector{}
		_ gorm.SavePointerDialectorInterface = Dialector{}
	)
}

// clauseStringBuilder is a minimal clause.Writer backed by a strings.Builder,
// used to capture QuoteTo and BindVarTo output directly without a full
// *gorm.Statement.
type clauseStringBuilder struct {
	buf []byte
}

func (w *clauseStringBuilder) WriteByte(c byte) error {
	w.buf = append(w.buf, c)
	return nil
}

func (w *clauseStringBuilder) WriteString(s string) (int, error) {
	w.buf = append(w.buf, s...)
	return len(s), nil
}

func (w *clauseStringBuilder) String() string {
	return string(w.buf)
}
