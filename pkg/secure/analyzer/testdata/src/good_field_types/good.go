// Package good_field_types exercises non-sensitive types that look like
// sensitive field names but whose Go type is not string or []byte.
// No diagnostics expected.
package good_field_types

import "golang.org/x/crypto/ssh"

// SafeFields uses fixed-size arrays and pointer/interface types for
// sensitive-looking names; none of them should be flagged.
type SafeFields struct {
	// Fixed-size array: isSensitiveType returns false (t.Len != nil).
	Passphrase [32]byte
	// Slice of a non-byte type: isSensitiveType returns false.
	Password []ssh.Signer
	// Pointer type: isSensitiveType returns false (neither Ident nor ArrayType).
	PIN *string
	// Integer field: isSensitiveType returns false (Ident but Name != "string").
	Secret int
}
