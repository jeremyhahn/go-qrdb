// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package dek provides Data Encryption Key (DEK) management for go-qrdb.
// It supports cluster-wide and per-tenant DEK lifecycle operations including
// generation, rotation, wrapping/unwrapping, and cryptographic shredding.
package dek

import "errors"

// NilInputError indicates a required parameter was nil.
type NilInputError struct {
	Op    string
	Field string
}

func (e *NilInputError) Error() string {
	return e.Op + ": " + e.Field + " must not be nil"
}

// UnsupportedAlgorithmError indicates an unsupported encryption algorithm.
type UnsupportedAlgorithmError struct {
	Op        string
	Algorithm string
}

func (e *UnsupportedAlgorithmError) Error() string {
	return e.Op + ": unsupported algorithm: " + e.Algorithm
}

// SealedError indicates the barrier is in sealed state and cannot perform
// the requested operation.
type SealedError struct {
	Op string
}

func (e *SealedError) Error() string {
	return e.Op + ": barrier is sealed"
}

// CryptoError wraps a cryptographic operation failure.
type CryptoError struct {
	Op     string
	Detail string
	Err    error
}

func (e *CryptoError) Error() string {
	msg := e.Op + ": " + e.Detail
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *CryptoError) Unwrap() error {
	return e.Err
}

// NotInitializedError indicates a resource has not been initialized.
type NotInitializedError struct {
	Op       string
	Resource string
}

func (e *NotInitializedError) Error() string {
	return e.Op + ": " + e.Resource + " not initialized"
}

// AlreadyInProgressError indicates an operation is already running.
type AlreadyInProgressError struct {
	Op       string
	Resource string
}

func (e *AlreadyInProgressError) Error() string {
	return e.Op + ": " + e.Resource + " already in progress"
}

// NoActiveOperationError indicates no active operation exists to cancel.
type NoActiveOperationError struct {
	Op       string
	Resource string
}

func (e *NoActiveOperationError) Error() string {
	return e.Op + ": no active " + e.Resource + " operation"
}

// NotImplementedError indicates a feature is not yet implemented.
type NotImplementedError struct {
	Message string
}

func (e *NotImplementedError) Error() string {
	return "not implemented: " + e.Message
}

// ManagerClosedError indicates the manager has been closed.
type ManagerClosedError struct {
	Op       string
	Resource string
}

func (e *ManagerClosedError) Error() string {
	return e.Op + ": " + e.Resource + " is closed"
}

// NotFoundError indicates a resource was not found.
type NotFoundError struct {
	Resource string
	ID       string
}

func (e *NotFoundError) Error() string {
	return e.Resource + " not found: " + e.ID
}

// DecodeError wraps a decoding failure.
type DecodeError struct {
	Op       string
	Resource string
	Err      error
}

func (e *DecodeError) Error() string {
	msg := e.Op + ": failed to decode " + e.Resource
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *DecodeError) Unwrap() error {
	return e.Err
}

// EncodeError wraps an encoding failure.
type EncodeError struct {
	Op       string
	Resource string
	Err      error
}

func (e *EncodeError) Error() string {
	msg := e.Op + ": failed to encode " + e.Resource
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *EncodeError) Unwrap() error {
	return e.Err
}

// StorageOperationError wraps a storage backend failure.
type StorageOperationError struct {
	Op     string
	Detail string
	Err    error
}

func (e *StorageOperationError) Error() string {
	msg := e.Op + ": " + e.Detail
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *StorageOperationError) Unwrap() error {
	return e.Err
}

// CiphertextTooShortError indicates the ciphertext is shorter than expected.
type CiphertextTooShortError struct {
	Op     string
	Detail string
}

func (e *CiphertextTooShortError) Error() string {
	return e.Op + ": ciphertext too short: " + e.Detail
}

// DataTruncatedError indicates decoded data was truncated.
type DataTruncatedError struct {
	Field   string
	Message string
}

func (e *DataTruncatedError) Error() string {
	if e.Field != "" {
		return e.Field + " " + e.Message
	}
	return "data truncated: " + e.Message
}

// RequiredFieldError indicates a required field was empty or missing.
type RequiredFieldError struct {
	Field string
}

func (e *RequiredFieldError) Error() string {
	return "required field missing: " + e.Field
}

// InvalidInputError wraps an invalid input error.
type InvalidInputError struct {
	Op  string
	Err error
}

func (e *InvalidInputError) Error() string {
	msg := e.Op + ": invalid input"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *InvalidInputError) Unwrap() error {
	return e.Err
}

// IsNotFound reports whether err is a *NotFoundError.
func IsNotFound(err error) bool {
	var nfe *NotFoundError
	return errors.As(err, &nfe)
}
