// Package secure is a stub of go-qrdb's pkg/secure: just enough surface for
// the analyzer's rules to resolve types against, with none of the behaviour.
package secure

import "github.com/jeremyhahn/go-enclave"

// Buffer stands in for the mlocked working copy.
type Buffer struct{ b []byte }

// Bytes is the unborrowed accessor rule 4 governs.
func (b *Buffer) Bytes() []byte { return b.b }

// Bytes is a package-level function sharing the method's name.
func Bytes() []byte { return nil }

// WithBytes is the borrowed accessor.
func (b *Buffer) WithBytes(fn func([]byte) error) error { return fn(b.b) }

// Wipe is inferred a consumer: it hands its argument to enclave.Wipe.
func Wipe(b []byte) { // want Wipe:"consumes\\(0\\)"
	enclave.Wipe(b)
}

// NewBufferFromBytes is inferred a consumer through Wipe.
func NewBufferFromBytes(src []byte) (*Buffer, error) { // want NewBufferFromBytes:"consumes\\(0\\)"
	b := &Buffer{b: make([]byte, len(src))}
	copy(b.b, src)
	Wipe(src)
	return b, nil
}

// CopyOut is inferred to return a copy: it copies a view into out and
// returns it.
func CopyOut(b *Buffer) ([]byte, error) { // want CopyOut:"returnsCopy\\(0\\)"
	var out []byte
	err := b.WithBytes(func(src []byte) error {
		out = make([]byte, len(src))
		copy(out, src)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
