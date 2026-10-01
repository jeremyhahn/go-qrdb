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
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// sessionSetRe matches PostgreSQL's SET [SESSION] name {= | TO} value. SET
// LOCAL is deliberately not matched: a transaction-scoped setting has no
// counterpart here, and approximating it as a session setting would let it
// outlive the transaction that set it.
var sessionSetRe = regexp.MustCompile(`(?is)^SET\s+(?:SESSION\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|\s+TO\s+)\s*(.+?)\s*;?\s*$`)

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

// routeSessionSet rewrites a SET the engine must see -- any qrdb_* setting,
// and lock_timeout -- into the engine's MySQL spelling. ok is false for any
// other statement, which the caller handles as before.
func routeSessionSet(statement string) (routed string, ok bool, err error) {
	m := sessionSetRe.FindStringSubmatch(statement)
	if m == nil {
		return "", false, nil
	}
	name, value := strings.ToLower(m[1]), strings.TrimSpace(m[2])
	if strings.HasPrefix(name, "qrdb_") {
		return "SET " + name + " = " + settingValue(value), true, nil
	}
	route, known := routedSettings[name]
	if !known {
		return "", false, nil
	}
	routed, err = route(name, value)
	return routed, true, err
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
