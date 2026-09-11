package scope_test_file

import (
	"github.com/awnumar/memguard" // want `import of "github.com/awnumar/memguard" is forbidden outside pkg/secure; use github.com/jeremyhahn/go-qrdb/pkg/secure instead`
	"golang.org/x/crypto/ssh"
)

// A fixture holding a literal from this file's own source. Rule 3 does not
// apply: the literal is in the binary's read-only data before the field
// exists, so the field's type moves nothing.
type fixture struct {
	Password   string
	Passphrase []byte
}

// Rules 1 and 2 DO apply in a test file, and this is the asymmetry S4 turns
// on. The one live breach on main when the gate was armed was a raw memguard
// import in a test.
func drive() {
	_ = memguard.NewBuffer(32)
	_, _ = ssh.ParsePrivateKey(nil) // want `call to ssh.ParsePrivateKey is forbidden; use ssh.ParsePrivateKeyWithPassphrase on \*secure\.Buffer-backed bytes`
	_ = fixture{}
}
