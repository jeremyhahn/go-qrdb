package good

import "golang.org/x/crypto/ssh"

// Credentials uses safe types; no diagnostics expected.
type Credentials struct {
	Username string
	Name     string
}

// Unrelated fields named like sensitive ones but not on the banned list
// are not flagged.
type Other struct {
	Prefix string
	Suffix string
}

func use(pem []byte, pass []byte) {
	_, _ = ssh.ParsePrivateKeyWithPassphrase(pem, pass)
}
