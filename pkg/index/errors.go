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

package index

import "strconv"

// ClosedError is returned when an operation is attempted on a closed index.
type ClosedError struct {
	Name string
}

// Error returns a human-readable description of the closed index.
func (e *ClosedError) Error() string {
	return "index: closed: " + e.Name
}

// NilDBError is returned when a nil PebbleDB is passed to the constructor.
type NilDBError struct{}

// Error returns a human-readable description of the nil database.
func (e *NilDBError) Error() string {
	return "index: pebble db is nil"
}

// EmptyNameError is returned when an empty index name is provided.
type EmptyNameError struct{}

// Error returns a human-readable description of the empty name.
func (e *EmptyNameError) Error() string {
	return "index: name must not be empty"
}

// NilFieldValueError is returned when a nil field value is passed.
type NilFieldValueError struct {
	Op string
}

// Error returns a human-readable description of the nil field value.
func (e *NilFieldValueError) Error() string {
	return "index: nil field value in " + e.Op
}

// UniqueViolationError is returned when a unique index constraint is violated.
type UniqueViolationError struct {
	IndexName   string
	FieldValue  []byte
	ExistingKey []byte
	NewKey      []byte
}

// Error returns a human-readable description of the unique constraint violation.
func (e *UniqueViolationError) Error() string {
	return "index: unique constraint violation on " + e.IndexName +
		": value " + string(e.FieldValue) + " already exists"
}

// InvalidNumericBoundError is returned when a Scan boundary on a numeric
// index cannot be parsed as a number. Unlike Put/Get/Delete — where an
// unparseable field value is treated as "this entry has no representable
// numeric value" and silently skipped — an unparseable Scan boundary is a
// caller programming error (a malformed query bound) and must never be
// reported as an empty-but-valid result set.
type InvalidNumericBoundError struct {
	IndexName string
	Op        string
	Value     []byte
	Err       error
}

// Error returns a human-readable description of the invalid scan bound.
func (e *InvalidNumericBoundError) Error() string {
	return "index: invalid numeric scan bound for " + e.IndexName +
		" (" + e.Op + "): " + string(e.Value) + ": " + e.Err.Error()
}

// Unwrap returns the underlying parse error for errors.Is/errors.As chains.
func (e *InvalidNumericBoundError) Unwrap() error {
	return e.Err
}

// KeyTooShortError is returned when an index key is too short to parse.
// NumberRejection names why text offered to a numeric index is not a number
// the index can order.
type NumberRejection string

const (
	// NumberSyntax is text that is not a decimal number or an infinity.
	NumberSyntax NumberRejection = "not a decimal number"

	// NumberIsNaN is NaN, which has no place in the index's total order.
	NumberIsNaN NumberRejection = "NaN is unordered and is not indexed"

	// NumberExponentRange is a number whose decimal exponent does not fit
	// in an int32.
	NumberExponentRange NumberRejection = "decimal exponent out of range"
)

// InvalidNumberError is returned when text offered to a numeric index is not
// a number the index can order. As a field value it means the record has no
// indexable value and is skipped; as a scan bound it is wrapped in an
// InvalidNumericBoundError and refused.
type InvalidNumberError struct {
	Value  string
	Reason NumberRejection
}

// Error returns a human-readable description of the rejected number.
func (e *InvalidNumberError) Error() string {
	return "index: invalid number " + strconv.Quote(e.Value) + ": " + string(e.Reason)
}

// CorruptNumericTermError is returned when bytes read back from a numeric
// index are not a term EncodeNumericValue writes.
type CorruptNumericTermError struct {
	Term   []byte
	Reason string
}

// Error returns a human-readable description of the corrupt term.
func (e *CorruptNumericTermError) Error() string {
	return "index: corrupt numeric term " + strconv.Quote(string(e.Term)) + ": " + e.Reason
}

// StaleEncodingError is returned when a numeric index definition records a
// term encoding other than NumericEncodingVersion. Its stored terms do not
// sort in numeric order under this binary; the definition must be rebuilt --
// entries cleared and backfilled at the current version -- not served.
type StaleEncodingError struct {
	IndexName string
	Version   int
}

// Error returns a human-readable description of the stale definition.
func (e *StaleEncodingError) Error() string {
	return "index: numeric index " + e.IndexName + " has encoding version " +
		strconv.Itoa(e.Version) + ", want " + strconv.Itoa(NumericEncodingVersion) +
		"; rebuild it"
}

type KeyTooShortError struct{}

// Error returns a human-readable description of the short key.
func (e *KeyTooShortError) Error() string {
	return "index: key too short to extract entity key"
}

// KeyPrefixMismatchError is returned when a key doesn't match the expected prefix.
type KeyPrefixMismatchError struct{}

// Error returns a human-readable description of the prefix mismatch.
func (e *KeyPrefixMismatchError) Error() string {
	return "index: key prefix does not match index"
}

// OwnershipError reports that an index create or an owner-checked index drop
// named an index recorded under a different owner (go-qrdb#426).
//
// On create it is the refusal that replaced silent adoption: index creation is
// idempotent by name, and before ownership was recorded a collection whose
// derived index name matched an index the explicit API had created took that
// index over -- with the existing definition, not its own -- and destroyed it
// on drop. On an owner-checked drop it reports that the index is not the
// caller's to remove, and nothing was removed.
type OwnershipError struct {
	// Name is the index name.
	Name string

	// Owner is the owner recorded on the index. The empty string is the
	// explicit index API.
	Owner string

	// Requested is the owner the refused command claimed.
	Requested string
}

// Error returns a human-readable description of the ownership conflict.
func (e *OwnershipError) Error() string {
	return "index: " + e.Name + " is owned by " + OwnerLabel(e.Owner) +
		", not " + OwnerLabel(e.Requested)
}

// NotFoundError is returned by a query or scan naming an index that does not
// exist: never created, or dropped. It is a lookup that missed, not a failed
// read, and the service layer reports it as such so every transport answers
// NotFound rather than an internal error.
type NotFoundError struct {
	// Name is the index name the caller asked for.
	Name string
}

// Error returns a human-readable description of the missing index.
func (e *NotFoundError) Error() string {
	return "index: " + e.Name + " not found"
}
