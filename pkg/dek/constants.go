// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

const (
	// AlgorithmAES256GCM is the default encryption algorithm.
	AlgorithmAES256GCM = "AES-256-GCM"

	// AESKeySize is the default AES-256 key size in bytes.
	AESKeySize = 32
)

// validAlgorithms is a map-based lookup for O(1) algorithm validation.
var validAlgorithms = map[string]bool{
	AlgorithmAES256GCM: true,
	"aes256-gcm":       true,
	"AES256-GCM":       true,
}

// IsValidAlgorithm reports whether the given algorithm string is supported.
func IsValidAlgorithm(algorithm string) bool {
	return validAlgorithms[algorithm]
}
