package bad_ssh

import "golang.org/x/crypto/ssh"

func use(pem []byte) {
	_, _ = ssh.ParsePrivateKey(pem) // want `call to ssh\.ParsePrivateKey is forbidden.*ParsePrivateKeyWithPassphrase`
}
