// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package api

import (
	"fmt"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// =============================================================================
// Storage Layer Error Types
// =============================================================================

// NewStorageClosedError creates a QRDBError for a closed storage backend.
//
// It wraps storage.ClosedError, the one closed-store error type in the repo,
// so errors.Is(err, storage.ErrStorageClosed) holds through the QRDBError as
// well as through the raw backend error. A local clone of the type used to
// live here; it was a second spelling of the same fact that nothing could
// match against, which is how go-qrdb#371 ended up being classified by string.
func NewStorageClosedError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &storage.ClosedError{},
	}
}

// ValidationError indicates a validation failure with a descriptive message.
type ValidationError struct {
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ValidationError) Unwrap() error {
	return e.Cause
}

// NewValidationError creates a QRDBError for a validation failure.
func NewValidationError(op, message string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ValidationError{Message: message},
	}
}

// NewValidationCauseError creates a QRDBError for a validation failure with a cause.
func NewValidationCauseError(op, message string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ValidationError{Message: message, Cause: cause},
	}
}

// CipherError indicates a cryptographic cipher operation failure.
type CipherError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *CipherError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Operation, e.Cause)
	}
	return e.Operation
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *CipherError) Unwrap() error {
	return e.Cause
}

// NewCipherError creates a QRDBError for a cipher operation failure.
func NewCipherError(op, operation string, code dberrors.ErrorCode, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &CipherError{Operation: operation, Cause: cause},
	}
}

// KeySizeError indicates an encryption key has an invalid size.
type KeySizeError struct {
	Algorithm string
	Expected  int
	Actual    int
}

// Error implements the error interface.
func (e *KeySizeError) Error() string {
	return fmt.Sprintf("invalid key size for %s: expected %d bytes, got %d bytes",
		e.Algorithm, e.Expected, e.Actual)
}

// NewKeySizeError creates a QRDBError for an invalid encryption key size.
func NewKeySizeError(op, algorithm string, expected, actual int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KeySizeError{Algorithm: algorithm, Expected: expected, Actual: actual},
	}
}

// KeyTooSmallError indicates an encryption master key is too small for the algorithm.
type KeyTooSmallError struct {
	Algorithm string
	MinSize   int
	Actual    int
}

// Error implements the error interface.
func (e *KeyTooSmallError) Error() string {
	return fmt.Sprintf("master key too small for %s: expected at least %d bytes, got %d bytes",
		e.Algorithm, e.MinSize, e.Actual)
}

// NewKeyTooSmallError creates a QRDBError for a master key that is too small.
func NewKeyTooSmallError(op, algorithm string, minSize, actual int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KeyTooSmallError{Algorithm: algorithm, MinSize: minSize, Actual: actual},
	}
}

// FieldEncryptionError indicates a failure encrypting or decrypting a specific field.
type FieldEncryptionError struct {
	FieldName string
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *FieldEncryptionError) Error() string {
	return fmt.Sprintf("failed to %s field '%s': %v", e.Operation, e.FieldName, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *FieldEncryptionError) Unwrap() error {
	return e.Cause
}

// NewFieldEncryptionError creates a QRDBError for a field encryption/decryption failure.
func NewFieldEncryptionError(op, fieldName, operation string, code dberrors.ErrorCode, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &FieldEncryptionError{FieldName: fieldName, Operation: operation, Cause: cause},
	}
}

// InvalidKeyEncodingError indicates a binary key could not be decoded.
type InvalidKeyEncodingError struct {
	KeyType string
	Detail  string
}

// Error implements the error interface.
func (e *InvalidKeyEncodingError) Error() string {
	return fmt.Sprintf("invalid %s key: %s", e.KeyType, e.Detail)
}

// NewInvalidKeyEncodingError creates a QRDBError for a key decoding failure.
func NewInvalidKeyEncodingError(op, keyType, detail string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidKeyEncodingError{KeyType: keyType, Detail: detail},
	}
}

// UnknownKeyPrefixError indicates an unrecognized key prefix byte.
type UnknownKeyPrefixError struct {
	Prefix byte
}

// Error implements the error interface.
func (e *UnknownKeyPrefixError) Error() string {
	return fmt.Sprintf("unknown key prefix: %c (0x%02x)", e.Prefix, e.Prefix)
}

// NewUnknownKeyPrefixError creates a QRDBError for an unknown key prefix.
func NewUnknownKeyPrefixError(op string, prefix byte) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownKeyPrefixError{Prefix: prefix},
	}
}

// UnknownCompareTargetError indicates an unrecognized comparison target.
type UnknownCompareTargetError struct {
	Target int
}

// Error implements the error interface.
func (e *UnknownCompareTargetError) Error() string {
	return fmt.Sprintf("unknown compare target: %d", e.Target)
}

// NewUnknownCompareTargetError creates a QRDBError for an unknown comparison target.
func NewUnknownCompareTargetError(op string, target int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownCompareTargetError{Target: target},
	}
}

// UnknownCompareResultError indicates an unrecognized comparison result operator.
type UnknownCompareResultError struct {
	Result int
}

// Error implements the error interface.
func (e *UnknownCompareResultError) Error() string {
	return fmt.Sprintf("unknown compare result: %d", e.Result)
}

// NewUnknownCompareResultError creates a QRDBError for an unknown comparison result.
func NewUnknownCompareResultError(op string, result int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownCompareResultError{Result: result},
	}
}

// UnsupportedValueTypeError indicates an unsupported value type for comparison.
type UnsupportedValueTypeError struct {
	TypeName string
}

// Error implements the error interface.
func (e *UnsupportedValueTypeError) Error() string {
	return "unsupported value type: " + e.TypeName
}

// NewUnsupportedValueTypeError creates a QRDBError for an unsupported comparison value type.
func NewUnsupportedValueTypeError(op, typeName string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedValueTypeError{TypeName: typeName},
	}
}

// InvalidLengthError indicates a field has an unexpected byte length.
type InvalidLengthError struct {
	Field    string
	Expected int
	Actual   int
}

// Error implements the error interface.
func (e *InvalidLengthError) Error() string {
	return fmt.Sprintf("invalid %s length: expected %d, got %d", e.Field, e.Expected, e.Actual)
}

// NewInvalidLengthError creates a QRDBError for an invalid field length.
func NewInvalidLengthError(op, field string, expected, actual int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidLengthError{Field: field, Expected: expected, Actual: actual},
	}
}

// RevisionBeforeCreationError indicates a requested revision is before the key was created.
type RevisionBeforeCreationError struct {
	Revision       int64
	CreateRevision int64
}

// Error implements the error interface.
func (e *RevisionBeforeCreationError) Error() string {
	return fmt.Sprintf("revision %d is before key creation at %d", e.Revision, e.CreateRevision)
}

// NewRevisionBeforeCreationError creates a QRDBError for requesting a revision before key creation.
func NewRevisionBeforeCreationError(op string, revision, createRevision int64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &RevisionBeforeCreationError{Revision: revision, CreateRevision: createRevision},
	}
}

// UnknownOperationTypeError indicates an unknown operation type in a transaction.
type UnknownOperationTypeError struct {
	OpType int
}

// Error implements the error interface.
func (e *UnknownOperationTypeError) Error() string {
	return fmt.Sprintf("unknown operation type: %d", e.OpType)
}

// NewUnknownOperationTypeError creates a QRDBError for an unknown transaction operation type.
func NewUnknownOperationTypeError(op string, opType int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownOperationTypeError{OpType: opType},
	}
}

// InvalidEncryptionAlgorithmError indicates an unrecognized encryption algorithm in a struct tag.
type InvalidEncryptionAlgorithmError struct {
	Algorithm string
	FieldName string
	Supported []string
}

// Error implements the error interface.
func (e *InvalidEncryptionAlgorithmError) Error() string {
	if len(e.Supported) > 0 {
		return fmt.Sprintf("invalid encryption algorithm '%s' for field '%s': supported values are '%s'",
			e.Algorithm, e.FieldName, storageJoinStrings(e.Supported))
	}
	return fmt.Sprintf("invalid encryption algorithm '%s' for field '%s'", e.Algorithm, e.FieldName)
}

// NewInvalidEncryptionAlgorithmError creates a QRDBError for an invalid encryption algorithm tag.
func NewInvalidEncryptionAlgorithmError(op, algorithm, fieldName string, supported []string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidEncryptionAlgorithmError{Algorithm: algorithm, FieldName: fieldName, Supported: supported},
	}
}

// UnsupportedFieldTypeError indicates a field type is not supported for an operation.
type UnsupportedFieldTypeError struct {
	FieldName string
	FieldType string
}

// Error implements the error interface.
func (e *UnsupportedFieldTypeError) Error() string {
	if e.FieldType != "" {
		return fmt.Sprintf("field '%s' has unsupported type '%s' for encryption: only string and []byte are supported",
			e.FieldName, e.FieldType)
	}
	return fmt.Sprintf("field '%s' has unsupported type for encryption", e.FieldName)
}

// NewUnsupportedFieldTypeError creates a QRDBError for an unsupported field type.
func NewUnsupportedFieldTypeError(op, fieldName, fieldType string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedFieldTypeError{FieldName: fieldName, FieldType: fieldType},
	}
}

// ExpectedStructError indicates a struct type was expected but a different kind was received.
type ExpectedStructError struct {
	Actual string
}

// Error implements the error interface.
func (e *ExpectedStructError) Error() string {
	return "expected struct, got " + e.Actual
}

// NewExpectedStructError creates a QRDBError for a non-struct type where a struct was required.
func NewExpectedStructError(op, actual string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ExpectedStructError{Actual: actual},
	}
}

// ExpectedStructTypeError indicates a struct type was expected for type-level reflection.
type ExpectedStructTypeError struct {
	Actual string
}

// Error implements the error interface.
func (e *ExpectedStructTypeError) Error() string {
	return "expected struct type, got " + e.Actual
}

// NewExpectedStructTypeError creates a QRDBError for a non-struct reflect.Type.
func NewExpectedStructTypeError(op, actual string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ExpectedStructTypeError{Actual: actual},
	}
}

// InvalidUint64EncodingError indicates a uint64 value could not be decoded from bytes.
type InvalidUint64EncodingError struct {
	Expected int
	Actual   int
}

// Error implements the error interface.
func (e *InvalidUint64EncodingError) Error() string {
	return fmt.Sprintf("invalid uint64 encoding: expected %d bytes, got %d", e.Expected, e.Actual)
}

// NewInvalidUint64EncodingError creates a QRDBError for an invalid uint64 encoding.
func NewInvalidUint64EncodingError(op string, expected, actual int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidUint64EncodingError{Expected: expected, Actual: actual},
	}
}

// PointerRequiredError indicates a pointer was expected but a non-pointer was provided.
type PointerRequiredError struct{}

// Error implements the error interface.
func (e *PointerRequiredError) Error() string {
	return "entity must be a pointer"
}

// NewPointerRequiredError creates a QRDBError for a non-pointer argument.
func NewPointerRequiredError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &PointerRequiredError{},
	}
}

// JSONMarshalError indicates a JSON marshaling failure.
type JSONMarshalError struct {
	Cause error
}

// Error implements the error interface.
func (e *JSONMarshalError) Error() string {
	return "json marshal failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *JSONMarshalError) Unwrap() error {
	return e.Cause
}

// NewJSONMarshalError creates a QRDBError for a JSON marshal failure.
func NewJSONMarshalError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &JSONMarshalError{Cause: cause},
	}
}

// JSONUnmarshalError indicates a JSON unmarshaling failure.
type JSONUnmarshalError struct {
	Cause error
}

// Error implements the error interface.
func (e *JSONUnmarshalError) Error() string {
	return "json unmarshal failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *JSONUnmarshalError) Unwrap() error {
	return e.Cause
}

// NewJSONUnmarshalError creates a QRDBError for a JSON unmarshal failure.
func NewJSONUnmarshalError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &JSONUnmarshalError{Cause: cause},
	}
}

// Base64DecodeError indicates a base64 decoding failure for a specific field.
type Base64DecodeError struct {
	FieldName string
	Cause     error
}

// Error implements the error interface.
func (e *Base64DecodeError) Error() string {
	return fmt.Sprintf("failed to decode base64 for field '%s': %v", e.FieldName, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *Base64DecodeError) Unwrap() error {
	return e.Cause
}

// NewBase64DecodeError creates a QRDBError for a base64 decoding failure.
func NewBase64DecodeError(op, fieldName string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrDecryption,
		Op:   op,
		Err:  &Base64DecodeError{FieldName: fieldName, Cause: cause},
	}
}

// UnsupportedEncryptionAlgorithmError indicates an unsupported encryption algorithm was requested.
type UnsupportedEncryptionAlgorithmError struct {
	Algorithm string
	Supported []string
}

// Error implements the error interface.
func (e *UnsupportedEncryptionAlgorithmError) Error() string {
	if len(e.Supported) > 0 {
		return fmt.Sprintf("unsupported algorithm: %s (supported: %s)",
			e.Algorithm, storageJoinStrings(e.Supported))
	}
	return "unsupported algorithm: " + e.Algorithm
}

// NewUnsupportedEncryptionAlgorithmError creates a QRDBError for an unsupported encryption algorithm.
func NewUnsupportedEncryptionAlgorithmError(op, algorithm string, supported []string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedEncryptionAlgorithmError{Algorithm: algorithm, Supported: supported},
	}
}

// KeyDerivationNotImplementedError indicates key derivation is not yet implemented.
type KeyDerivationNotImplementedError struct{}

// Error implements the error interface.
func (e *KeyDerivationNotImplementedError) Error() string {
	return "key derivation not yet implemented"
}

// NewKeyDerivationNotImplementedError creates a QRDBError for unimplemented key derivation.
func NewKeyDerivationNotImplementedError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnimplemented,
		Op:   op,
		Err:  &KeyDerivationNotImplementedError{},
	}
}

// InvalidBatchPointError indicates a validation failure at a specific point index in a batch.
type InvalidBatchPointError struct {
	Index int
	Cause error
}

// Error implements the error interface.
func (e *InvalidBatchPointError) Error() string {
	return fmt.Sprintf("invalid point at index %d: %v", e.Index, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidBatchPointError) Unwrap() error {
	return e.Cause
}

// NewInvalidBatchPointError creates a QRDBError for a batch point validation failure.
func NewInvalidBatchPointError(op string, index int, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidBatchPointError{Index: index, Cause: cause},
	}
}

// storageJoinStrings joins string slice elements with ", " separator.
func storageJoinStrings(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}
