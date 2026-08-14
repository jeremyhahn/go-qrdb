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

package catalog

import (
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/planbuilder"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestColumnTypeOf_RoundTrip(t *testing.T) {
	cases := []string{
		"TINYINT",
		"TINYINT UNSIGNED",
		"BOOLEAN",
		"SMALLINT",
		"SMALLINT UNSIGNED",
		"MEDIUMINT",
		"MEDIUMINT UNSIGNED",
		"INT",
		"INT UNSIGNED",
		"BIGINT",
		"BIGINT UNSIGNED",
		"FLOAT",
		"DOUBLE",
		"DECIMAL(10,2)",
		"DECIMAL(5,0)",
		"CHAR(10)",
		"VARCHAR(255)",
		"TINYTEXT",
		"TEXT",
		"MEDIUMTEXT",
		"LONGTEXT",
		"BINARY(16)",
		"VARBINARY(255)",
		"TINYBLOB",
		"BLOB",
		"MEDIUMBLOB",
		"LONGBLOB",
		"DATE",
		"DATETIME",
		"DATETIME(6)",
		"TIMESTAMP",
		"TIMESTAMP(3)",
		"TIME",
		"YEAR",
		"BIT(8)",
		"JSON",
		"ENUM('a','b','c')",
		"SET('x','y')",
	}
	for _, typ := range cases {
		t.Run(typ, func(t *testing.T) {
			col := service.ColumnDef{Name: "c", Type: typ}
			sqlType, err := ColumnTypeOf(col)
			if err != nil {
				t.Fatalf("ColumnTypeOf(%q): %v", typ, err)
			}
			if sqlType == nil {
				t.Fatalf("ColumnTypeOf(%q) returned nil type", typ)
			}
			got := TypeStringOf(sqlType)
			if got != typ {
				t.Fatalf("TypeStringOf round trip: got %q, want %q", got, typ)
			}
		})
	}
}

// TestColumnTypeOf_IntDefaults verifies INT and BIGINT round trip without
// requiring the UNSIGNED suffix when signed.
func TestColumnTypeOf_IntegerAliases(t *testing.T) {
	col := service.ColumnDef{Name: "c", Type: "INTEGER"}
	sqlType, err := ColumnTypeOf(col)
	if err != nil {
		t.Fatalf("ColumnTypeOf(INTEGER): %v", err)
	}
	if TypeStringOf(sqlType) != "INT" {
		t.Fatalf("INTEGER should resolve to INT, got %q", TypeStringOf(sqlType))
	}

	col = service.ColumnDef{Name: "c", Type: "BOOL"}
	sqlType, err = ColumnTypeOf(col)
	if err != nil {
		t.Fatalf("ColumnTypeOf(BOOL): %v", err)
	}
	if TypeStringOf(sqlType) != "BOOLEAN" {
		t.Fatalf("BOOL should resolve to BOOLEAN, got %q", TypeStringOf(sqlType))
	}
}

// TestColumnTypeOf_Defaults verifies types with optional parameters default
// per MySQL's own rules when parameters are omitted.
func TestColumnTypeOf_Defaults(t *testing.T) {
	cases := map[string]string{
		"CHAR":    "CHAR(1)",
		"BINARY":  "BINARY(1)",
		"DECIMAL": "DECIMAL(10,0)",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			sqlType, err := ColumnTypeOf(service.ColumnDef{Name: "c", Type: in})
			if err != nil {
				t.Fatalf("ColumnTypeOf(%q): %v", in, err)
			}
			if got := TypeStringOf(sqlType); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestColumnTypeOf_SpatialTypesRejected(t *testing.T) {
	spatial := []string{
		"GEOMETRY", "POINT", "LINESTRING", "POLYGON",
		"MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON", "GEOMETRYCOLLECTION",
	}
	for _, typ := range spatial {
		t.Run(typ, func(t *testing.T) {
			_, err := ColumnTypeOf(service.ColumnDef{Name: "geom", Type: typ})
			var spatialErr *SpatialTypeUnsupportedError
			if !errors.As(err, &spatialErr) {
				t.Fatalf("expected *SpatialTypeUnsupportedError, got %T (%v)", err, err)
			}
		})
	}
}

func TestColumnTypeOf_UnknownType(t *testing.T) {
	_, err := ColumnTypeOf(service.ColumnDef{Name: "c", Type: "NOT_A_TYPE"})
	var unsupported *UnsupportedColumnTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *UnsupportedColumnTypeError, got %T (%v)", err, err)
	}
}

func TestColumnTypeOf_MalformedTypeStrings(t *testing.T) {
	malformed := []string{
		"",
		"   ",
		"VARCHAR(255",
		"DECIMAL(abc,2)",
		"VARCHAR()",
		"BIT",
		"ENUM()",
		"SET()",
		"ENUM(a,b)",
		"ENUM('a','b",
		"CHAR(-1)",
		"VARCHAR(-1)",
		"BINARY(-1)",
		"VARBINARY(-1)",
		"BIT(0)",
		"BIT(65)",
		"DATETIME(7)",
		"TIMESTAMP(9)",
		"DECIMAL(5,abc)",
		"DECIMAL(3,5)",
	}
	for _, typ := range malformed {
		t.Run(typ, func(t *testing.T) {
			_, err := ColumnTypeOf(service.ColumnDef{Name: "c", Type: typ})
			if err == nil {
				t.Fatalf("ColumnTypeOf(%q): expected error, got nil", typ)
			}
		})
	}
}

func TestColumnTypeOf_VarcharRequiresLength(t *testing.T) {
	_, err := ColumnTypeOf(service.ColumnDef{Name: "c", Type: "VARCHAR"})
	if err == nil {
		t.Fatal("expected error for VARCHAR without a length")
	}
}

func TestColumnTypeOf_VarbinaryRequiresLength(t *testing.T) {
	_, err := ColumnTypeOf(service.ColumnDef{Name: "c", Type: "VARBINARY"})
	if err == nil {
		t.Fatal("expected error for VARBINARY without a length")
	}
}

func TestSplitQuotedList_EscapedQuote(t *testing.T) {
	vals, err := splitQuotedList(`'it''s','plain'`)
	if err != nil {
		t.Fatalf("splitQuotedList: %v", err)
	}
	if len(vals) != 2 || vals[0] != "it's" || vals[1] != "plain" {
		t.Fatalf("unexpected split: %#v", vals)
	}
}

func TestSchemaOf(t *testing.T) {
	def := &service.TableDef{
		Name: "users",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "email", Type: "VARCHAR(255)", Nullable: false},
			{Name: "bio", Type: "TEXT", Nullable: true, Comment: "user bio"},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}

	schema, err := SchemaOf(def)
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	if len(schema) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(schema))
	}
	if !schema[0].PrimaryKey {
		t.Fatal("id should be marked PrimaryKey")
	}
	if !schema[0].AutoIncrement {
		t.Fatal("id should be marked AutoIncrement")
	}
	if schema[1].Nullable {
		t.Fatal("email should not be nullable")
	}
	if !schema[2].Nullable {
		t.Fatal("bio should be nullable")
	}
	if schema[2].Comment != "user bio" {
		t.Fatalf("unexpected comment: %q", schema[2].Comment)
	}
	for _, c := range schema {
		if c.Source != "users" {
			t.Fatalf("expected Source=users, got %q", c.Source)
		}
	}
}

func TestSchemaOf_NilAndEmpty(t *testing.T) {
	if _, err := SchemaOf(nil); err == nil {
		t.Fatal("expected error for nil TableDef")
	}
	var nilErr *NilTableDefError
	if _, err := SchemaOf(nil); !errors.As(err, &nilErr) {
		t.Fatalf("expected *NilTableDefError")
	}

	def := &service.TableDef{Name: "empty"}
	if _, err := SchemaOf(def); err == nil {
		t.Fatal("expected error for table with no columns")
	}
	var emptyErr *EmptyColumnsError
	if _, err := SchemaOf(def); !errors.As(err, &emptyErr) {
		t.Fatalf("expected *EmptyColumnsError")
	}
}

func TestSchemaOf_PropagatesColumnError(t *testing.T) {
	def := &service.TableDef{
		Name:    "bad",
		Columns: []service.ColumnDef{{Name: "c", Type: "NOT_A_TYPE"}},
	}
	if _, err := SchemaOf(def); err == nil {
		t.Fatal("expected error to propagate from ColumnTypeOf")
	}
}

func TestPrimaryKeySchemaOf(t *testing.T) {
	def := &service.TableDef{
		Name: "orders",
		Columns: []service.ColumnDef{
			{Name: "tenant_id", Type: "BIGINT UNSIGNED"},
			{Name: "order_id", Type: "BIGINT UNSIGNED"},
			{Name: "total", Type: "DECIMAL(10,2)"},
		},
		PrimaryKey: []service.PKColumn{{Name: "tenant_id"}, {Name: "order_id"}},
	}

	pk, err := PrimaryKeySchemaOf(def)
	if err != nil {
		t.Fatalf("PrimaryKeySchemaOf: %v", err)
	}
	if len(pk.PkOrdinals) != 2 || pk.PkOrdinals[0] != 0 || pk.PkOrdinals[1] != 1 {
		t.Fatalf("unexpected PK ordinals: %v", pk.PkOrdinals)
	}
}

func TestPrimaryKeySchemaOf_NoPrimaryKey(t *testing.T) {
	def := &service.TableDef{
		Name:    "log",
		Columns: []service.ColumnDef{{Name: "message", Type: "TEXT"}},
	}
	pk, err := PrimaryKeySchemaOf(def)
	if err != nil {
		t.Fatalf("PrimaryKeySchemaOf: %v", err)
	}
	if len(pk.PkOrdinals) != 0 {
		t.Fatalf("expected no PK ordinals, got %v", pk.PkOrdinals)
	}
}

func TestPrimaryKeySchemaOf_UnknownColumn(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "a", Type: "INT"}},
		PrimaryKey: []service.PKColumn{{Name: "does_not_exist"}},
	}
	_, err := PrimaryKeySchemaOf(def)
	var notFound *ColumnNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *ColumnNotFoundError, got %T (%v)", err, err)
	}
}

func TestTypeStringOf_Nil(t *testing.T) {
	if got := TypeStringOf(nil); got != "" {
		t.Fatalf("expected empty string for nil type, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Column defaults
// ---------------------------------------------------------------------------

// TestSchemaOf_Default verifies SchemaOf carries every persisted column
// default onto the go-mysql-server column verbatim -- the persisted text is
// already the canonical, self-describing form the engine's own parser
// round-trips: a literal as-is, an expression in parentheses, and the bare
// CURRENT_TIMESTAMP forms unparenthesized.
//
// Before this was wired up the persisted default had no reader at all on
// this path, so information_schema.COLUMN_DEFAULT reported NULL for every
// column and an INSERT omitting a column stored NULL instead of its
// declared default.
func TestSchemaOf_Default(t *testing.T) {
	cases := []struct {
		name string
		col  service.ColumnDef
		want string // "" means no default at all
	}{
		{
			name: "string literal",
			col:  service.ColumnDef{Name: "s", Type: "VARCHAR(64)", Default: "'null'", Nullable: true},
			want: "'null'",
		},
		{
			name: "numeric literal",
			col:  service.ColumnDef{Name: "n", Type: "BIGINT", Default: "7", Nullable: true},
			want: "7",
		},
		{
			name: "boolean literal",
			col:  service.ColumnDef{Name: "b", Type: "BOOLEAN", Default: "false", Nullable: true},
			want: "false",
		},
		{
			name: "parenthesized expression is carried verbatim",
			col:  service.ColumnDef{Name: "e", Type: "INT", Default: "(1 + 1)", Nullable: true},
			want: "(1 + 1)",
		},
		{
			name: "doubly parenthesized expression is carried verbatim",
			col:  service.ColumnDef{Name: "e", Type: "INT", Default: "((1 + 1))", Nullable: true},
			want: "((1 + 1))",
		},
		{
			name: "NOW() stays bare",
			col:  service.ColumnDef{Name: "t", Type: "DATETIME", Default: "NOW()", Nullable: true},
			want: "NOW()",
		},
		{
			name: "CURRENT_TIMESTAMP stays bare",
			col:  service.ColumnDef{Name: "t", Type: "DATETIME", Default: "CURRENT_TIMESTAMP", Nullable: true},
			want: "CURRENT_TIMESTAMP",
		},
		{
			name: "current_timestamp(6) is case insensitive",
			col:  service.ColumnDef{Name: "t", Type: "DATETIME(6)", Default: "current_timestamp(6)", Nullable: true},
			want: "current_timestamp(6)",
		},
		{
			name: "whitespace only is no default",
			col:  service.ColumnDef{Name: "s", Type: "VARCHAR(64)", Default: "   ", Nullable: true},
			want: "",
		},
		{
			name: "no default",
			col:  service.ColumnDef{Name: "s", Type: "VARCHAR(64)", Nullable: true},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := SchemaOf(&service.TableDef{Name: "t", Columns: []service.ColumnDef{tc.col}})
			if err != nil {
				t.Fatalf("SchemaOf: %v", err)
			}
			got := schema[0].Default
			if tc.want == "" {
				if got != nil {
					t.Fatalf("Default = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Default = nil, want %q", tc.want)
			}
			if got.Expr == nil {
				t.Fatal("Default.Expr = nil, want an unresolved placeholder")
			}
			if _, ok := got.Expr.(*sqle.UnresolvedColumnDefault); !ok {
				t.Fatalf("Default.Expr is %T, want *sql.UnresolvedColumnDefault so the engine's own parser resolves it", got.Expr)
			}
			if got.Expr.String() != tc.want {
				t.Fatalf("Default text = %q, want %q", got.Expr.String(), tc.want)
			}
		})
	}
}

// TestSchemaOf_DefaultResolvesThroughEngineParser verifies the text SchemaOf
// emits is text go-mysql-server's own default resolver accepts, and that the
// engine classifies each one as literal or expression purely from the
// parentheses in the persisted text -- which is what makes a separate stored
// discriminator unnecessary.
func TestSchemaOf_DefaultResolvesThroughEngineParser(t *testing.T) {
	cases := []struct {
		name        string
		col         service.ColumnDef
		wantLiteral bool
	}{
		{"literal", service.ColumnDef{Name: "n", Type: "BIGINT", Default: "7", Nullable: true}, true},
		{"expression", service.ColumnDef{Name: "n", Type: "BIGINT", Default: "(1 + 1)", Nullable: true}, false},
		{"doubly parenthesized expression", service.ColumnDef{Name: "n", Type: "BIGINT", Default: "((1 + 1))", Nullable: true}, false},
		{"bare temporal expression", service.ColumnDef{Name: "d", Type: "DATETIME", Default: "CURRENT_TIMESTAMP", Nullable: true}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := &service.TableDef{Name: "t", Columns: []service.ColumnDef{tc.col}}
			schema, err := SchemaOf(def)
			if err != nil {
				t.Fatalf("SchemaOf: %v", err)
			}

			ctx := sqle.NewEmptyContext()
			b := planbuilder.NewBuilderForColumnDefaultResolution(ctx, sqle.EngineOverrides{})
			b.SetParserOptions(sqle.NewSqlModeFromString("").ParserOptions())
			resolved := b.ResolveSchemaDefaults("db", "t", schema)

			cd := resolved[0].Default
			if cd == nil {
				t.Fatal("resolved default is nil")
			}
			if !cd.Resolved() {
				t.Fatalf("default %v did not resolve", cd)
			}
			if cd.IsLiteral() != tc.wantLiteral {
				t.Fatalf("IsLiteral() = %v, want %v", cd.IsLiteral(), tc.wantLiteral)
			}
			if _, err := cd.Eval(ctx, nil); err != nil {
				t.Fatalf("Eval: %v", err)
			}
		})
	}
}

// TestSchemaOf_Extra verifies the persisted Extra attribute reaches the
// go-mysql-server column, since information_schema.COLUMNS.EXTRA is read
// straight off it.
func TestSchemaOf_Extra(t *testing.T) {
	def := &service.TableDef{
		Name:    "t",
		Columns: []service.ColumnDef{{Name: "a", Type: "INT", Extra: "DEFAULT_GENERATED"}},
	}
	schema, err := SchemaOf(def)
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	if schema[0].Extra != "DEFAULT_GENERATED" {
		t.Fatalf("Extra = %q, want %q", schema[0].Extra, "DEFAULT_GENERATED")
	}
}

// TestDefaultIsExpression covers the discriminator that replaced the
// separately-persisted DefaultIsExpr flag: an expression is either
// parenthesized or one of the bare CURRENT_TIMESTAMP forms, and everything
// else is a literal. Only RowCodec.defaultFor consults it (every other
// reader hands the text to go-mysql-server's own parser), so its two false
// answers -- a quoted string that merely CONTAINS parentheses, and a bare
// function call that is not a temporal default -- are pinned here directly.
func TestDefaultIsExpression(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"parenthesized expression", "(1 + 1)", true},
		{"doubly parenthesized expression", "((1 + 1))", true},
		{"bare NOW()", "NOW()", true},
		{"bare CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP", true},
		{"bare current_timestamp(6) lowercase", "current_timestamp(6)", true},
		{"numeric literal", "7", false},
		{"negative numeric literal", "-7", false},
		{"quoted string literal", "'hi'", false},
		{"quoted string literal containing parens", "'(not an expression)'", false},
		{"boolean literal", "false", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultIsExpression(tc.text); got != tc.want {
				t.Fatalf("defaultIsExpression(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
