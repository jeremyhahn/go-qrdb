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

package driver

import (
	"database/sql"
	sqld "database/sql/driver"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Arguments of a defined (named) type.
//
// A type defined from a bindable type -- `type ID uint`, `type Num int64` --
// is how every ORM spells a typed column, and GORM's own models use exactly
// that idiom. Classification used to be a concrete type switch, which no
// defined type can match, so such an argument was rejected by
// CheckNamedValue before any SQL ran:
//
//	sql: converting argument $1 type: driver: cannot bind argument 0 of
//	type tests_test.ID
//
// bindings_test.go pins the conversion itself; the tests here pin it through
// the whole database/sql surface -- checker, bind expressions, engine, real
// relational table -- because that is the layer at which the failure was
// visible and a unit test alone would not have caught it.
// ---------------------------------------------------------------------------

// The defined types an ORM would generate for a model.
type (
	rowID     uint
	rowNum    int64
	rowLabel  string
	rowRatio  float32
	rowActive bool
	rowBlob   []byte
)

// TestEmbeddedRelational_DefinedTypeArguments verifies defined types bind
// through INSERT, WHERE and UPDATE, and that each column round-trips as the
// value that was bound.
func TestEmbeddedRelational_DefinedTypeArguments(t *testing.T) {
	db, _ := openRelationalTestDB(t, "definedtypes")

	const ddl = `CREATE TABLE models (
		id BIGINT UNSIGNED PRIMARY KEY,
		num BIGINT,
		label VARCHAR(64),
		ratio DOUBLE,
		active BOOLEAN,
		payload VARBINARY(32)
	)`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	if _, err := db.Exec(
		"INSERT INTO models (id, num, label, ratio, active, payload) VALUES (?, ?, ?, ?, ?, ?)",
		rowID(7), rowNum(18), rowLabel("gizmo"), rowRatio(0.5), rowActive(true), rowBlob{0x00, 0xFF},
	); err != nil {
		t.Fatalf("INSERT with defined-type arguments: %v", err)
	}

	var (
		num     int64
		label   string
		ratio   float64
		active  bool
		payload []byte
	)
	err := db.QueryRow(
		"SELECT num, label, ratio, active, payload FROM models WHERE id = ?", rowID(7),
	).Scan(&num, &label, &ratio, &active, &payload)
	if err != nil {
		t.Fatalf("SELECT with a defined-type argument: %v", err)
	}

	if num != 18 {
		t.Errorf("num = %d, want 18", num)
	}
	if label != "gizmo" {
		t.Errorf("label = %q, want gizmo", label)
	}
	if ratio != 0.5 {
		t.Errorf("ratio = %v, want 0.5", ratio)
	}
	if !active {
		t.Error("active = false, want true")
	}
	// The hex-literal binding of a byte slice must survive being defined:
	// bound as a string literal, 0xFF would be reinterpreted under the
	// connection charset.
	if string(payload) != "\x00\xff" {
		t.Errorf("payload = %x, want 00ff", payload)
	}

	res, err := db.Exec("UPDATE models SET num = ? WHERE id = ?", rowNum(19), rowID(7))
	if err != nil {
		t.Fatalf("UPDATE with defined-type arguments: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("RowsAffected = %d (err %v), want 1", n, err)
	}

	if err := db.QueryRow("SELECT num FROM models WHERE id = ?", rowID(7)).Scan(&num); err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if num != 19 {
		t.Errorf("num after update = %d, want 19", num)
	}
}

// TestEmbeddedRelational_DefinedTypeNoMatch verifies a defined-type argument
// that matches no row returns no rows rather than a coerced match. Widening the
// accepted set must not make the binding approximate.
func TestEmbeddedRelational_DefinedTypeNoMatch(t *testing.T) {
	db, _ := openRelationalTestDB(t, "definedtypesmiss")

	if _, err := db.Exec("CREATE TABLE models (id BIGINT UNSIGNED PRIMARY KEY, num BIGINT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO models (id, num) VALUES (?, ?)", rowID(7), rowNum(18)); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var num int64
	err := db.QueryRow("SELECT num FROM models WHERE id = ?", rowID(8)).Scan(&num)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error = %v, want sql.ErrNoRows", err)
	}
}

// TestEmbeddedRelational_UnbindableArgumentIsReported verifies an argument the
// driver cannot classify is still refused end to end. There is no stringifying
// fallback: a value bound as text the engine reinterprets is a silently wrong
// query rather than a failed one.
func TestEmbeddedRelational_UnbindableArgumentIsReported(t *testing.T) {
	db, _ := openRelationalTestDB(t, "definedtypesbad")

	if _, err := db.Exec("CREATE TABLE models (id BIGINT UNSIGNED PRIMARY KEY, num BIGINT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	_, err := db.Exec("INSERT INTO models (id, num) VALUES (?, ?)", rowID(1), struct{ A int }{})

	var unsupported *UnsupportedArgumentTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *UnsupportedArgumentTypeError", err)
	}
	if unsupported.Ordinal != 2 {
		t.Errorf("Ordinal = %d, want 2 (the offending argument's own position)", unsupported.Ordinal)
	}
}

// TestEmbeddedRelational_ValuerErrorIsReported verifies an error returned by a
// driver.Valuer reaches the caller instead of being swallowed or bound as
// NULL. GORM's TestInvalidValuer depends on exactly this: its EncryptedData
// type refuses to encode certain payloads, and the failing Create must fail.
func TestEmbeddedRelational_ValuerErrorIsReported(t *testing.T) {
	db, _ := openRelationalTestDB(t, "valuererr")

	if _, err := db.Exec("CREATE TABLE models (id BIGINT UNSIGNED PRIMARY KEY, secret VARBINARY(32))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	_, err := db.Exec("INSERT INTO models (id, secret) VALUES (?, ?)", rowID(1), refusingValuer{})
	if !errors.Is(err, errValuerRefused) {
		t.Fatalf("error = %v, want %v", err, errValuerRefused)
	}
}

// errValuerRefused is what refusingValuer reports.
var errValuerRefused = errors.New("driver: test valuer refused")

// refusingValuer is a driver.Valuer that fails, standing in for GORM's
// EncryptedData rejecting a payload it will not encode.
type refusingValuer struct{}

func (refusingValuer) Value() (sqld.Value, error) { return nil, errValuerRefused }
