// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package aesgcm

import "strconv"

// InvalidKeyLengthError is returned when the provided key is not the expected size.
type InvalidKeyLengthError struct {
	Expected int
	Actual   int
}

func (e *InvalidKeyLengthError) Error() string {
	return "aesgcm: invalid key length: expected " +
		strconv.Itoa(e.Expected) + ", got " + strconv.Itoa(e.Actual)
}

// CipherError is returned when an AES or GCM operation fails.
type CipherError struct {
	Op  string
	Err error
}

func (e *CipherError) Error() string {
	return "aesgcm " + e.Op + ": " + e.Err.Error()
}

func (e *CipherError) Unwrap() error { return e.Err }

// DecryptionError is returned when GCM authentication or decryption fails.
type DecryptionError struct {
	Err error
}

func (e *DecryptionError) Error() string {
	return "aesgcm decryption failed: " + e.Err.Error()
}

func (e *DecryptionError) Unwrap() error { return e.Err }

// DataTooShortError is returned when the ciphertext is shorter than the nonce.
type DataTooShortError struct {
	MinSize    int
	ActualSize int
}

func (e *DataTooShortError) Error() string {
	return "data too short: minimum " +
		strconv.Itoa(e.MinSize) + " bytes, got " + strconv.Itoa(e.ActualSize)
}
