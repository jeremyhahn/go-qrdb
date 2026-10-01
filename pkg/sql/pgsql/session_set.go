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

//go:build qrdb_pgsql

package pgsql

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// sessionSetRe matches PostgreSQL's SET [SESSION] name {= | TO} value. SET
// LOCAL is matched separately (localSetRe): a transaction-scoped setting has
// no counterpart here, and approximating it as a session setting would let
// it outlive the transaction that set it.
var sessionSetRe = regexp.MustCompile(`(?is)^SET\s+(?:SESSION\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|\s+TO\s+)\s*(.+?)\s*;?\s*$`)

// localSetRe matches PostgreSQL's SET LOCAL name, capturing the name.
var localSetRe = regexp.MustCompile(`(?is)^SET\s+LOCAL\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

// bareWordRe matches an unquoted identifier value (SET x TO pessimistic).
var bareWordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_+]*$`)

// lockTimeoutRe matches a PostgreSQL lock_timeout value: a number with an
// optional time unit, quoted or not.
var lockTimeoutRe = regexp.MustCompile(`(?i)^'?\s*(\d+(?:\.\d+)?)\s*(ms|s|min|h|d)?\s*'?$`)

// lockTimeoutUnits maps a PostgreSQL time unit to milliseconds; a bare
// number is milliseconds, PostgreSQL's base unit for lock_timeout.
var lockTimeoutUnits = map[string]float64{
	"":    1,
	"ms":  1,
	"s":   1000,
	"min": 60 * 1000,
	"h":   60 * 60 * 1000,
	"d":   24 * 60 * 60 * 1000,
}

// routedSettings are the PostgreSQL SET targets that change how this
// session's statements run, and the rewrite that carries each to the engine.
// Every other SET stays the no-op it has always been on this wire.
var routedSettings = map[string]func(name, value string) (string, error){
	"lock_timeout": routeLockTimeout,
}

// isRoutedSetting reports whether a SET of name must reach the engine.
func isRoutedSetting(name string) bool {
	if strings.HasPrefix(name, "qrdb_") {
		return true
	}
	_, known := routedSettings[name]
	return known
}

// routeSessionSet rewrites a SET the engine must see -- any qrdb_* setting,
// and lock_timeout -- into the engine's MySQL spelling. ok is false for any
// other statement, which the caller handles as before.
//
// statement must be one statement: the simple-query path splits a
// multi-statement text before it gets here. A text that still holds more
// than one -- one carrying bind parameters, which the split leaves whole --
// is refused when it starts with a routed SET, rather than read with the
// rest of the text as the setting's value. SET LOCAL of a routed setting is
// refused by name (see LocalSettingUnsupportedError).
func routeSessionSet(statement string) (routed string, ok bool, err error) {
	statements := engine.SplitTopLevelStatements(statement)
	first := statements[0]
	if m := localSetRe.FindStringSubmatch(first); m != nil {
		name := strings.ToLower(m[1])
		if !isRoutedSetting(name) {
			return "", false, nil
		}
		return "", true, &LocalSettingUnsupportedError{Name: name}
	}
	m := sessionSetRe.FindStringSubmatch(first)
	if m == nil {
		return "", false, nil
	}
	name, value := strings.ToLower(m[1]), strings.TrimSpace(m[2])
	if !isRoutedSetting(name) {
		return "", false, nil
	}
	if len(statements) > 1 {
		return "", true, &MultipleStatementsError{}
	}
	if strings.HasPrefix(name, "qrdb_") {
		return "SET " + name + " = " + settingValue(value), true, nil
	}
	routed, err = routedSettings[name](name, value)
	return routed, true, err
}

// sessionSetCodes maps each error routeSessionSet returns to its SQLSTATE.
var sessionSetCodes = []struct {
	match func(error) bool
	code  codes.Code
}{
	{func(err error) bool { var e *InvalidSettingError; return errors.As(err, &e) }, codes.InvalidParameterValue},
	{func(err error) bool { var e *LocalSettingUnsupportedError; return errors.As(err, &e) }, codes.FeatureNotSupported},
	{func(err error) bool { var e *MultipleStatementsError; return errors.As(err, &e) }, codes.Syntax},
}

// sessionSetWireError attaches the SQLSTATE of a routeSessionSet error.
func sessionSetWireError(err error) error {
	for _, c := range sessionSetCodes {
		if c.match(err) {
			return psqlerr.WithCode(err, c.code)
		}
	}
	return psqlerr.WithCode(err, codes.InvalidParameterValue)
}

// settingValue quotes a bare identifier value, which PostgreSQL accepts for
// an enumerated setting and MySQL would read as a column reference.
func settingValue(value string) string {
	if strings.EqualFold(value, "DEFAULT") || !bareWordRe.MatchString(value) {
		return value
	}
	return "'" + value + "'"
}

// routeLockTimeout translates PostgreSQL's lock_timeout onto MySQL's
// innodb_lock_wait_timeout: whole seconds, rounded UP so a timeout is never
// shortened, with 0 (PostgreSQL's "wait forever") mapped to the largest
// value innodb_lock_wait_timeout accepts.
func routeLockTimeout(name, value string) (string, error) {
	if strings.EqualFold(value, "DEFAULT") {
		return "SET " + provider.LockWaitTimeoutVariable + " = DEFAULT", nil
	}
	m := lockTimeoutRe.FindStringSubmatch(value)
	if m == nil {
		return "", &InvalidSettingError{Name: name, Value: value}
	}
	amount, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return "", &InvalidSettingError{Name: name, Value: value}
	}
	ms := amount * lockTimeoutUnits[strings.ToLower(m[2])]
	seconds := provider.MaxLockWaitTimeoutSeconds
	if ms > 0 {
		seconds = int64(math.Min(math.Ceil(ms/1000), float64(provider.MaxLockWaitTimeoutSeconds)))
	}
	return "SET " + provider.LockWaitTimeoutVariable + " = " + strconv.FormatInt(seconds, 10), nil
}

// LocalSettingUnsupportedError reports SET LOCAL of a setting this wire
// routes to the engine. The engine's settings are session-scoped only, and
// accepting the statement as a no-op would leave the client believing a
// transaction-scoped value is in force. It reaches a PostgreSQL client as
// SQLSTATE 0A000 (feature_not_supported).
type LocalSettingUnsupportedError struct {
	Name string
}

// Error names the setting and the supported spelling.
func (e *LocalSettingUnsupportedError) Error() string {
	return "sql/pgsql: SET LOCAL is not supported for parameter \"" + e.Name + "\"; use SET or SET SESSION"
}

// MultipleStatementsError reports a SET routed to the engine whose text
// holds more than one statement -- one the simple-query split did not
// separate because it carries bind parameters, which PostgreSQL refuses in
// a multi-statement text too. It reaches a PostgreSQL client as SQLSTATE
// 42601 (syntax_error), PostgreSQL's code for "cannot insert multiple
// commands into a prepared statement".
type MultipleStatementsError struct{}

// Error returns PostgreSQL's wording.
func (e *MultipleStatementsError) Error() string {
	return "sql/pgsql: cannot insert multiple commands into a prepared statement"
}

// InvalidSettingError reports a SET whose value the setting cannot take. It
// reaches a PostgreSQL client as SQLSTATE 22023 (invalid_parameter_value).
type InvalidSettingError struct {
	Name  string
	Value string
}

// Error returns PostgreSQL's wording.
func (e *InvalidSettingError) Error() string {
	return "sql/pgsql: invalid value for parameter \"" + e.Name + "\": " + e.Value
}
