// Package secure is a stub of go-qrdb's pkg/secure: just enough surface for
// the analyzer's rules to resolve types against, with none of the behaviour.
package secure

// Buffer stands in for the mlocked working copy.
type Buffer struct{ b []byte }

// Bytes is the unborrowed accessor rule 4 governs.
func (b *Buffer) Bytes() []byte { return b.b }

// WithBytes is the borrowed accessor.
func (b *Buffer) WithBytes(fn func([]byte) error) error { return fn(b.b) }
