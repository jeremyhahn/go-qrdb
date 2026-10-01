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

//go:build qrdb_pgsql

package pgsql

import (
	"errors"
	"regexp"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"

	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// rowLockNotAvailable holds the MySQL numbers PostgreSQL reports as
// lock_not_available (55P03): a lock wait that timed out, and NOWAIT.
var rowLockNotAvailable = map[int]bool{
	rowlock.SQLCodeLockWaitTimeout: true,
	rowlock.SQLCodeLockNowait:      true,
}

// wireQueryError wraps a failed statement's error for the PostgreSQL wire,
// attaching the standard SQLSTATE the failure class carries in PostgreSQL
// itself. Without a recognised code psql-wire reports every error as XXUUU,
// which no client understands -- most consequentially
// gorm.io/driver/postgres's error translator, which turns 23505 into
// gorm.ErrDuplicatedKey and 23503 into gorm.ErrForeignKeyViolated and
// passes anything else through untranslated, so ON CONFLICT upsert
// fallbacks and duplicate-detection application flows silently break.
//
// Only failure classes with an unambiguous PostgreSQL equivalent are
// mapped; everything else keeps psql-wire's internal-error default rather
// than guessing a code the client would then act on.
func wireQueryError(query string, err error) error {
	qerr := &QueryError{Query: query, Err: err}
	if code, ok := sqlStateFor(err); ok {
		return psqlerr.WithCode(qerr, code)
	}
	return qerr
}

// sqlStateFor maps an engine error to the SQLSTATE PostgreSQL would raise
// for the same condition, or ok=false when no unambiguous mapping exists.
//
// Each level of the error chain is classified separately because two of the
// links do not participate in Go's standard unwrapping:
// go-mysql-server's kind errors (gopkg.in/src-d/go-errors) only match
// Kind.Is when the kind error itself is at the top, and its
// sql.WrappedInsertError -- the wrapper every constraint violation raised
// during an INSERT actually arrives in -- has no Unwrap method at all, so
// its cause must be followed by hand.
func sqlStateFor(err error) (codes.Code, bool) {
	for err != nil {
		if code, ok := sqlStateForLevel(err); ok {
			return code, ok
		}
		switch e := err.(type) { //nolint:errorlint // deliberate: WrappedInsertError has NO Unwrap, this loop IS the hand-rolled chain walk
		case sql.WrappedInsertError:
			err = e.Cause
		default:
			err = errors.Unwrap(err)
		}
	}
	return "", false
}

// sqlStateForLevel classifies one level of the error chain.
func sqlStateForLevel(err error) (codes.Code, bool) {
	var (
		mysqlErr *mysql.SQLError
		aborted  *provider.TransactionAbortedError
		deadlock *rowlock.DeadlockError
	)
	switch {
	case errors.As(err, &mysqlErr) && mysqlErr.Num == provider.ErrCodeTableLayoutUnsupported:
		// A table created by v0.20.0 through v0.21.0, which this version
		// cannot read until it is dropped and re-created: PostgreSQL's
		// class for an object not in the state the operation requires.
		return codes.ObjectNotInPrerequisiteState, true
	case errors.As(err, &mysqlErr) && rowLockNotAvailable[mysqlErr.Num]:
		// A row-lock wait that timed out, or a NOWAIT that found the row
		// held: PostgreSQL reports both as lock_not_available.
		return codes.LockNotAvailable, true
	case errors.As(err, &mysqlErr) && mysqlErr.Num == provider.SQLCodeSkipLockedUnsupported:
		return codes.FeatureNotSupported, true
	case service.AsCause(err, &aborted):
		// A statement in a transaction already rolled back as a deadlock
		// victim. Checked before the deadlock itself, which is its cause,
		// and before the ER_LOCK_DEADLOCK wrapper that carries both.
		return codes.InFailedSQLTransaction, true
	case service.AsCause(err, &deadlock):
		// PostgreSQL names a deadlock apart from a serialization failure.
		return codes.DeadlockDetected, true
	case sql.ErrLockDeadlock.Is(err):
		// ErrLockDeadlock is go-mysql-server's own vocabulary for a
		// serialization failure (see that Kind's doc comment): a buffered
		// transaction's commit-time compare-and-swap lost a race, whether
		// to an ordinary write conflict (provider.TransactionConflictError)
		// or to a TRUNCATE advancing a table's generation out from under it
		// (service.TableGenerationStaleError, go-qrdb#547). PostgreSQL
		// reports both the same way -- 40001, serialization_failure -- and
		// so does MySQL (ER_LOCK_DEADLOCK/1213, via go-mysql-server's own
		// wire translation), so one case here covers whichever cause
		// QRDBSession.CommitTransaction wrapped.
		return codes.SerializationFailure, true
	case sql.ErrUniqueKeyViolation.Is(err),
		sql.ErrPrimaryKeyViolation.Is(err),
		sql.ErrDuplicateEntry.Is(err):
		return codes.UniqueViolation, true
	case sql.ErrForeignKeyChildViolation.Is(err),
		sql.ErrForeignKeyParentViolation.Is(err):
		return codes.ForeignKeyViolation, true
	case sql.ErrCheckConstraintViolated.Is(err):
		return codes.CheckViolation, true
	case sql.ErrTableNotFound.Is(err):
		return codes.UndefinedTable, true
	case sql.ErrColumnNotFound.Is(err), sql.ErrTableColumnNotFound.Is(err):
		return codes.UndefinedColumn, true
	case sql.ErrInsertIntoNonNullableProvidedNull.Is(err):
		return codes.NotNullViolation, true
	}
	return "", false
}

// insertIgnorePrefixRe matches the INSERT IGNORE statements the translator
// produces from PostgreSQL's ON CONFLICT ... DO NOTHING.
var insertIgnorePrefixRe = regexp.MustCompile(`(?i)^\s*INSERT\s+IGNORE\b`)

// pgSuppressedViolationCodes are the MySQL warning codes INSERT IGNORE may
// legitimately record on the PostgreSQL wire: the duplicate-key code is the
// ONE failure class PostgreSQL's ON CONFLICT DO NOTHING actually
// suppresses. Every other ignorable class -- foreign-key violations, CHECK
// violations, NULL into NOT NULL -- still errors in PostgreSQL, so an
// IGNORE that swallowed one must be converted back into the statement
// failure the client expects. Without this, a join-table INSERT ... ON
// CONFLICT DO NOTHING referencing a missing parent row "succeeded" with
// zero rows instead of raising the FK violation gorm's association tests
// assert on.
var pgSuppressedViolationCodes = map[int]bool{
	1062: true, // ER_DUP_ENTRY: unique/primary key conflict
}

// sqlStateByMySQLWarningCode maps a re-raised warning's MySQL code to the
// SQLSTATE PostgreSQL reports for that condition.
var sqlStateByMySQLWarningCode = map[int]codes.Code{
	1452: codes.ForeignKeyViolation, // ER_NO_REFERENCED_ROW_2 (child insert)
	1451: codes.ForeignKeyViolation, // ER_ROW_IS_REFERENCED_2 (parent delete)
	3819: codes.CheckViolation,      // ER_CHECK_CONSTRAINT_VIOLATED
	1048: codes.NotNullViolation,    // ER_BAD_NULL_ERROR
}

// IgnoredViolationError is the statement failure re-raised when INSERT
// IGNORE suppressed a violation PostgreSQL's ON CONFLICT DO NOTHING would
// not have.
type IgnoredViolationError struct {
	Code    int
	Message string
}

// Error returns a human-readable description.
func (e *IgnoredViolationError) Error() string {
	return e.Message
}

// checkIgnoredViolations inspects the warnings an INSERT IGNORE execution
// recorded past baseline (the session's warning count before it ran) and
// returns the first violation PostgreSQL would have raised, or nil when
// every suppressed conflict was a genuine duplicate-key. Warnings() is
// newest-first, so the new entries are the leading ones; the OLDEST new
// warning is returned so the reported failure is the first one that
// occurred, matching statement order.
func checkIgnoredViolations(sqlCtx *sql.Context, translated string, baseline int) error {
	if !insertIgnorePrefixRe.MatchString(translated) {
		return nil
	}
	warnings := sqlCtx.Warnings()
	fresh := len(warnings) - baseline
	if fresh <= 0 {
		return nil
	}
	for i := fresh - 1; i >= 0; i-- {
		warning := warnings[i]
		if pgSuppressedViolationCodes[warning.Code] {
			continue
		}
		verr := &IgnoredViolationError{Code: warning.Code, Message: warning.Message}
		if code, ok := sqlStateByMySQLWarningCode[warning.Code]; ok {
			return psqlerr.WithCode(verr, code)
		}
		return verr
	}
	return nil
}
