// Package scope_codec_tag exercises S2, the transport/DTO clause: a field the
// decoder materialises is out of scope for rule 3, and a field that merely
// sits in the same struct is not.
package scope_codec_tag

// AtTheWire is the shape of every flagged handler request struct on main.
// No diagnostics expected for the tagged fields.
type AtTheWire struct {
	Passphrase string `json:"passphrase"`
	Secret     []byte `json:"secret,omitempty"`
	Password   string `yaml:"password"`
	PIN        string `msgpack:"pin"`
	PrivateKey []byte `bson:"private_key"`
	// A codec key the list does not name leaves the field IN scope. The
	// exemption is the enumerated set in codecTags, not "has a tag".
	KeyMaterial []byte `gorm:"column:key_material"` // want `field "KeyMaterial" holds sensitive material as \[\]byte; switch to \*secure\.Secret or \*secure\.Buffer`
	// An untagged neighbour in the same struct is still reported.
	RecoveryShare string // want `field "RecoveryShare" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
}

// NotAtTheWire proves the clause READS the tag rather than noticing one.
// json:"-" means the field is never serialized, so no decoder materialises it
// and the exemption's whole premise is absent.
type NotAtTheWire struct {
	Secret     string `json:"-"`                // want `field "Secret" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
	Passphrase []byte `json:"-,omitempty"`      // want `field "Passphrase" holds sensitive material as \[\]byte; switch to \*secure\.Secret or \*secure\.Buffer`
	Password   string `json:",omitempty"`       // named by the field name, so it IS on the wire
	PIN        string `json:"-" yaml:"the_pin"` // a second codec DOES carry it
}

// TagWithoutCodec carries a struct tag that names no codec at all. A tag is
// not evidence of a boundary; a tag NAMING A CODEC is. The clause fails
// CLOSED on anything it cannot read as one.
type TagWithoutCodec struct {
	Secret string `this tag names no codec` // want `field "Secret" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
}
