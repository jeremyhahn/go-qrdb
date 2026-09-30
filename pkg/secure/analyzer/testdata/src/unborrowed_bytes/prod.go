package unborrowed_bytes

import (
	"bytes"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

type encrypter interface {
	Encrypt(plaintext []byte) ([]byte, error)
}

func bare(buf *secure.Buffer, e encrypter) {
	_, _ = e.Encrypt(buf.Bytes()) // want `secure\.Buffer\.Bytes holds no borrow across the read`
}

func directiveAbove(buf *secure.Buffer, e encrypter) {
	//secure:unborrowed injected-interface e is supplied by the caller
	_, _ = e.Encrypt(buf.Bytes())
}

func directiveSameLine(buf *secure.Buffer, e encrypter) {
	_, _ = e.Encrypt(buf.Bytes()) //secure:unborrowed injected-interface e is supplied by the caller
}

func nestedEnclaveKind(buf *secure.Buffer) []byte {
	//secure:unborrowed nested-enclave inside a go-quicraft enclave borrow
	return buf.Bytes()
}

func directiveTooFarAbove(buf *secure.Buffer, e encrypter) {
	//secure:unborrowed injected-interface a directive two lines up covers nothing

	_, _ = e.Encrypt(buf.Bytes()) // want `secure\.Buffer\.Bytes holds no borrow across the read`
}

// A directive trailing a DIFFERENT statement on the line above justifies
// nothing: only a comment standing on a line of its own covers the next line.
func directiveTrailingAnotherStatement(buf *secure.Buffer, e encrypter) {
	n := 1                            //secure:unborrowed injected-interface this trails n, not the read below
	_, _ = e.Encrypt(buf.Bytes()[:n]) // want `secure\.Buffer\.Bytes holds no borrow across the read`
}

// The read spans a protected-memory allocation, which rule 6 forbids inside a
// borrow.
func allocatesProtectedKind(buf *secure.Buffer) []byte {
	//secure:unborrowed allocates-protected the callee copies the key into an enclave of its own
	return buf.Bytes()
}

func unknownKind(buf *secure.Buffer, e encrypter) {
	//secure:unborrowed convenience it was easier
	_, _ = e.Encrypt(buf.Bytes()) // want `malformed //secure:unborrowed directive`
}

func noReason(buf *secure.Buffer, e encrypter) {
	//secure:unborrowed injected-interface
	_, _ = e.Encrypt(buf.Bytes()) // want `malformed //secure:unborrowed directive`
}

func noSpace(buf *secure.Buffer, e encrypter) {
	//secure:unborrowedinjected-interface glued to the prefix
	_, _ = e.Encrypt(buf.Bytes()) // want `malformed //secure:unborrowed directive`
}

func methodValue(buf *secure.Buffer) func() []byte {
	return buf.Bytes // want `secure\.Buffer\.Bytes holds no borrow across the read`
}

func methodExpr() func(*secure.Buffer) []byte {
	return (*secure.Buffer).Bytes // want `secure\.Buffer\.Bytes holds no borrow across the read`
}

// Borrowed reads are what the rule points to.
func borrowed(buf *secure.Buffer, e encrypter) error {
	return buf.WithBytes(func(b []byte) error {
		_, err := e.Encrypt(b)
		return err
	})
}

// A Bytes method on any other type is not the protected accessor.
func otherBytes(b *bytes.Buffer) []byte {
	return b.Bytes()
}

// A package-level function that happens to be called Bytes is not the method.
func packageFunc() []byte { return secure.Bytes() }

// A field named Bytes is not the method.
type holder struct{ Bytes []byte }

func field(h holder) []byte { return h.Bytes }

// A value-receiver Bytes on an unrelated named type is not the method either.
type view []byte

func (v view) Bytes() []byte { return v }

func valueReceiver(v view) []byte { return v.Bytes() }
