package bad_field

type Credentials struct {
	Username   string
	Passphrase string // want `field "Passphrase" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
	Password   []byte // want `field "Password" holds sensitive material as \[\]byte; switch to \*secure\.Secret or \*secure\.Buffer`
	PIN        string // want `field "PIN" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
}

type Other struct {
	privateKey []byte // want `field "privateKey" holds sensitive material as \[\]byte; switch to \*secure\.Secret or \*secure\.Buffer`
	Name       string
}
