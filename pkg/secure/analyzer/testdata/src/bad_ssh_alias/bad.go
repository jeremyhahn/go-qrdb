// Package bad_ssh_alias tests that aliased ssh imports are also flagged.
package bad_ssh_alias

import cryptossh "golang.org/x/crypto/ssh"

func use(pem []byte) {
	_, _ = cryptossh.ParsePrivateKey(pem) // want `call to cryptossh\.ParsePrivateKey is forbidden.*ParsePrivateKeyWithPassphrase`
}
