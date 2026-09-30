// Package enclave is a stub of go-enclave: the two functions securecopy
// treats as root consumers.
package enclave

// Wipe zeroes b.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Sealed stands in for a sealed enclave.
type Sealed struct{ n int }

// NewSealed takes ownership of src and wipes it.
func NewSealed(src []byte) (*Sealed, error) {
	s := &Sealed{n: len(src)}
	for i := range src {
		src[i] = 0
	}
	return s, nil
}
