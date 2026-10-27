// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vlog

import (
	"bytes"
	"crypto"
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The checkpoint a log's head is published as is a C2SP signed note
// (c2sp.org/signed-note) whose text is a C2SP tlog-checkpoint
// (c2sp.org/tlog-checkpoint), both at the revision docs/reference/ pins:
//
//	<origin>
//	<tree size>
//	<base64 root hash>
//
//	— <key name> <base64(key hash || signature)>

// noteSignaturePrefix opens every signature line: an em dash (U+2014) and a
// space.
const noteSignaturePrefix = "— "

// maxNoteSignatures bounds the signature lines a checkpoint may carry.
// signed-note requires a verifier to accept at least 16.
const maxNoteSignatures = 16

// NoteSignature is one signature line of a signed note.
type NoteSignature struct {
	// KeyName is the name of the key that signed.
	KeyName string
	// KeyHash is the key's 32-bit key ID, as KeyHash computes it.
	KeyHash uint32
	// Signature is the signature over the note text, without the key hash.
	Signature []byte
}

// Checkpoint is a parsed checkpoint: the note text's three tlog-checkpoint
// lines, and the note's signature lines. Text is the note text, final
// newline included, which every signature covers.
type Checkpoint struct {
	Origin     string
	Size       uint64
	Root       []byte
	Text       []byte
	Signatures []NoteSignature
}

// ValidKeyName reports whether name may name a key in a signature line:
// non-empty, valid UTF-8, and without a Unicode space, a plus (U+002B) or a
// control character.
func ValidKeyName(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsFunc(name, func(r rune) bool {
		return r == '+' || unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

// RenderCheckpoint returns the checkpoint for h over origin as a signed
// note whose one signature line names keyName and keyHash.
func RenderCheckpoint(origin string, h SignedHead, keyName string, keyHash uint32) ([]byte, error) {
	if !ValidKeyName(keyName) {
		return nil, &HeadSignatureError{Reason: "invalid key name " + strconv.Quote(keyName)}
	}
	if len(h.Signature) == 0 {
		return nil, &HeadSignatureError{Reason: "empty signature"}
	}
	text := CheckpointBody(origin, h.Size, h.Root)
	sig := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(h.Signature)), keyHash)
	sig = append(sig, h.Signature...)
	note := make([]byte, 0, len(text)+len(noteSignaturePrefix)+len(keyName)+base64.StdEncoding.EncodedLen(len(sig))+3)
	note = append(note, text...)
	note = append(note, '\n')
	note = append(note, noteSignaturePrefix...)
	note = append(note, keyName...)
	note = append(note, ' ')
	note = base64.StdEncoding.AppendEncode(note, sig)
	note = append(note, '\n')
	if _, err := ParseCheckpoint(note); err != nil {
		return nil, err
	}
	return note, nil
}

// ParseCheckpoint parses note as a signed note whose text is a
// tlog-checkpoint, refusing any note either specification does not admit.
// It verifies no signature.
func ParseCheckpoint(note []byte) (*Checkpoint, error) {
	text, sigs, err := parseNote(note)
	if err != nil {
		return nil, err
	}
	c := &Checkpoint{Text: text, Signatures: sigs}
	if err := c.parseText(); err != nil {
		return nil, err
	}
	return c, nil
}

// parseNote splits note, a signed note, into its text, final newline
// included, and its signature lines.
func parseNote(note []byte) ([]byte, []NoteSignature, error) {
	if !utf8.Valid(note) || bytes.ContainsFunc(note, func(r rune) bool { return r < 0x20 && r != '\n' }) {
		return nil, nil, malformedCheckpoint("not UTF-8 without control characters")
	}
	split := bytes.LastIndex(note, []byte("\n\n"))
	if split < 0 {
		return nil, nil, malformedCheckpoint("no blank line before the signatures")
	}
	sigs, err := parseNoteSignatures(note[split+2:])
	if err != nil {
		return nil, nil, err
	}
	return note[:split+1], sigs, nil
}

// parseNoteSignatures parses a signed note's signature lines.
func parseNoteSignatures(b []byte) ([]NoteSignature, error) {
	if len(b) == 0 || b[len(b)-1] != '\n' {
		return nil, malformedCheckpoint("signature lines must end in a newline")
	}
	lines := strings.Split(string(b[:len(b)-1]), "\n")
	if len(lines) > maxNoteSignatures {
		return nil, malformedCheckpoint("more than " + strconv.Itoa(maxNoteSignatures) + " signatures")
	}
	sigs := make([]NoteSignature, 0, len(lines))
	for _, line := range lines {
		rest, ok := strings.CutPrefix(line, noteSignaturePrefix)
		if !ok {
			return nil, malformedCheckpoint("signature line does not start with an em dash and a space")
		}
		name, encoded, ok := strings.Cut(rest, " ")
		if !ok || !ValidKeyName(name) {
			return nil, malformedCheckpoint("signature line has no valid key name")
		}
		raw, err := decodeCanonicalBase64(encoded)
		if err != nil || len(raw) <= 4 {
			return nil, malformedCheckpoint("signature is not canonical base64 of a key hash and a signature")
		}
		sig := NoteSignature{KeyName: name, KeyHash: binary.BigEndian.Uint32(raw), Signature: raw[4:]}
		for _, prior := range sigs {
			if prior.KeyName == sig.KeyName && prior.KeyHash == sig.KeyHash {
				return nil, malformedCheckpoint("two signatures by key " + name)
			}
		}
		sigs = append(sigs, sig)
	}
	return sigs, nil
}

// parseText parses c.Text as a tlog-checkpoint: origin, decimal tree size
// and base64 root hash, then optional non-empty extension lines.
func (c *Checkpoint) parseText() error {
	lines := strings.Split(string(c.Text[:len(c.Text)-1]), "\n")
	if len(lines) < 3 {
		return malformedCheckpoint("fewer than three lines of text")
	}
	for _, line := range lines {
		if line == "" {
			return malformedCheckpoint("empty line in the text")
		}
	}
	size, err := strconv.ParseUint(lines[1], 10, 64)
	if err != nil || strconv.FormatUint(size, 10) != lines[1] {
		return malformedCheckpoint("tree size is not an ASCII decimal")
	}
	root, err := decodeCanonicalBase64(lines[2])
	if err != nil || len(root) == 0 {
		return malformedCheckpoint("root hash is not canonical base64")
	}
	c.Origin, c.Size, c.Root = lines[0], size, root
	return nil
}

// Head returns c as the head sig signs, with the key version and signature
// type of the key that verified it.
func (c *Checkpoint) Head(sig NoteSignature, version uint32, t SignatureType) SignedHead {
	return SignedHead{Size: c.Size, Root: bytes.Clone(c.Root), KeyVersion: version, SignatureType: t, Signature: bytes.Clone(sig.Signature)}
}

// verify checks the signature lines c carries from the key named keyName
// with public key pub. A line from that key that does not verify fails the
// checkpoint, as signed-note requires; found is false when c carries no line
// from the key.
func (c *Checkpoint) verify(keyName string, pub crypto.PublicKey) (sig NoteSignature, t SignatureType, found bool, err error) {
	t, err = publicSignatureType(pub)
	if err != nil {
		return NoteSignature{}, "", false, err
	}
	hash := keyHashes[t](keyName, pub)
	want := binary.BigEndian.Uint32(hash)
	for _, s := range c.Signatures {
		if s.KeyName != keyName || s.KeyHash != want {
			continue
		}
		if !verifyFuncs[t](pub, c.Text, s.Signature) {
			return NoteSignature{}, "", false, &HeadSignatureError{Reason: "signature by key " + keyName + " does not verify"}
		}
		return s, t, true, nil
	}
	return NoteSignature{}, "", false, nil
}

// VerifyCheckpoint parses note, a checkpoint for origin, and verifies it
// under the key named keyName with public key pub. Signature lines from
// other keys are ignored; the note fails unless one from this key verifies.
// The head it returns carries no key version.
func VerifyCheckpoint(note []byte, origin, keyName string, pub crypto.PublicKey) (SignedHead, error) {
	c, err := ParseCheckpoint(note)
	if err != nil {
		return SignedHead{}, err
	}
	if c.Origin != origin {
		return SignedHead{}, &HeadSignatureError{Reason: "checkpoint origin " + strconv.Quote(c.Origin) + ", want " + strconv.Quote(origin)}
	}
	sig, t, found, err := c.verify(keyName, pub)
	if err != nil {
		return SignedHead{}, err
	}
	if !found {
		return SignedHead{}, &HeadSignatureError{Reason: "no signature by key " + keyName}
	}
	return c.Head(sig, 0, t), nil
}

// VerifyCheckpoint parses note, a checkpoint for the log, and verifies it
// under whichever of the log's key versions signed it, the key named
// keyName: a checkpoint signed before a rotation still verifies under the
// version that signed it. The head it returns carries that version.
func (d Descriptor) VerifyCheckpoint(keyName string, note []byte) (SignedHead, error) {
	c, err := ParseCheckpoint(note)
	if err != nil {
		return SignedHead{}, err
	}
	if c.Origin != d.Origin {
		return SignedHead{}, &HeadSignatureError{Reason: "checkpoint origin " + strconv.Quote(c.Origin) + ", want " + strconv.Quote(d.Origin)}
	}
	for _, key := range d.Keys {
		pub, err := key.Public()
		if err != nil {
			return SignedHead{}, err
		}
		sig, t, found, err := c.verify(keyName, pub)
		if err != nil {
			return SignedHead{}, err
		}
		if found {
			return c.Head(sig, key.Version, t), nil
		}
	}
	return SignedHead{}, &HeadSignatureError{Reason: "no signature by a key of the log named " + keyName}
}

// decodeCanonicalBase64 decodes s as standard padded base64, refusing an
// encoding that is not canonical (RFC 4648, Section 3.5).
func decodeCanonicalBase64(s string) ([]byte, error) {
	return base64.StdEncoding.Strict().DecodeString(s)
}

// malformedCheckpoint is the error for a note ParseCheckpoint refuses.
func malformedCheckpoint(reason string) error {
	return &HeadSignatureError{Reason: "malformed checkpoint: " + reason}
}
