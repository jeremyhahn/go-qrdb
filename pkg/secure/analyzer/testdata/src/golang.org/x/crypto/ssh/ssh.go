// Minimal stub for analyzer testdata. Not a functional implementation.
package ssh

type Signer interface{}

func ParsePrivateKey(pem []byte) (Signer, error) {
	return nil, nil
}

func ParsePrivateKeyWithPassphrase(pem, pass []byte) (Signer, error) {
	return nil, nil
}
