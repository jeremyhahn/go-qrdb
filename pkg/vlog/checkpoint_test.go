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
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// deviceCheckpoint signs a head of size 1 over origin with key k held by p
// and returns it with its checkpoint.
func deviceCheckpoint(t *testing.T, p *deviceProvider, origin string, k HeadKey) (SignedHead, []byte) {
	t.Helper()
	signer, err := p.HeadSigner(context.Background(), HeadSignerRequest{
		TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, KeyRef: k.Ref, KeyVersion: k.Version})
	if err != nil {
		t.Fatalf("HeadSigner: %v", err)
	}
	h, err := SignHead(context.Background(), signer, origin, 1, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("SignHead: %v", err)
	}
	note, err := RenderCheckpoint(origin, h, signer.Name(), signer.KeyHash())
	if err != nil {
		t.Fatalf("RenderCheckpoint: %v", err)
	}
	return h, note
}

// TestParseNote_SignedNoteSpecVector verifies the example signed note of
// c2sp.org/signed-note under the example verifier key it publishes.
func TestParseNote_SignedNoteSpecVector(t *testing.T) {
	const vkey = "example.com/foo+530d903a+AekyeRrm56hApGFkyQR4ZCbV54Id2LKaANYcrnKv3U2k"
	const note = "This is an example message.\n\n" +
		"— example.com/foo Uw2QOkn8srV1yJGh2VYRlL1Tnagv1YEq6TfXppzi2ONncAlTgK7Ztg1ERYNZXsYjOBH3mFXmRKuwHjG1Yu72IneyaQM=\n"
	parts := strings.Split(vkey, "+")
	material, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || material[0] != 0x01 {
		t.Fatalf("vkey material = %x, %v", material, err)
	}
	pub := ed25519.PublicKey(material[1:])
	text, sigs, err := parseNote([]byte(note))
	if err != nil {
		t.Fatalf("parseNote: %v", err)
	}
	if string(text) != "This is an example message.\n" || len(sigs) != 1 || sigs[0].KeyName != parts[0] {
		t.Fatalf("parseNote = %q, %+v", text, sigs)
	}
	if hash, _ := KeyHash(parts[0], pub); sigs[0].KeyHash != hash || hash != 0x530d903a {
		t.Fatalf("key hash = %08x, KeyHash = %08x, want 530d903a", sigs[0].KeyHash, hash)
	}
	if !verifyFuncs[SignatureEd25519](pub, text, sigs[0].Signature) {
		t.Fatal("the spec's example signature does not verify")
	}
}

// TestParseCheckpoint_TlogCheckpointSpecExample parses the example
// checkpoint of c2sp.org/tlog-checkpoint.
func TestParseCheckpoint_TlogCheckpointSpecExample(t *testing.T) {
	const note = "example.com/behind-the-sofa\n20852163\nCsUYapGGPo4dkMgIAUqom/Xajj7h2fB2MPA3j2jxq2I=\n\n" +
		"— example.com/behind-the-sofa Az3grlgtzPICa5OS8npVmf1Myq/5IZniMp+ZJurmRDeOoRDe4URYN7u5/Zhcyv2q1gGzGku9nTo+zyWE+xeMcTOAYQ8=\n"
	c, err := ParseCheckpoint([]byte(note))
	if err != nil {
		t.Fatalf("ParseCheckpoint: %v", err)
	}
	root, _ := base64.StdEncoding.DecodeString("CsUYapGGPo4dkMgIAUqom/Xajj7h2fB2MPA3j2jxq2I=")
	if c.Origin != "example.com/behind-the-sofa" || c.Size != 20852163 || !bytes.Equal(c.Root, root) {
		t.Fatalf("checkpoint = %q %d %x", c.Origin, c.Size, c.Root)
	}
	if len(c.Signatures) != 1 || c.Signatures[0].KeyName != "example.com/behind-the-sofa" || c.Signatures[0].KeyHash != 0x033de0ae || len(c.Signatures[0].Signature) != ed25519.SignatureSize {
		t.Fatalf("signatures = %+v", c.Signatures)
	}
}

func TestRenderCheckpoint_IsTheSignedNote(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			p := newDeviceProvider()
			k := deviceKey(t, p, st, FirstKeyVersion)
			h, note := deviceCheckpoint(t, p, "db.example.com/acme/certs", k)
			text, line, ok := strings.Cut(string(note), "\n\n")
			if !ok || text+"\n" != string(CheckpointBody("db.example.com/acme/certs", h.Size, h.Root)) {
				t.Fatalf("note text = %q", text)
			}
			if !strings.HasPrefix(line, "— "+testLog.String()+" ") || !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
				t.Fatalf("signature line = %q", line)
			}
			pub, err := k.Public()
			if err != nil {
				t.Fatalf("Public: %v", err)
			}
			got, err := VerifyCheckpoint(note, "db.example.com/acme/certs", testLog.String(), pub)
			if err != nil {
				t.Fatalf("VerifyCheckpoint: %v", err)
			}
			if got.Size != h.Size || !bytes.Equal(got.Root, h.Root) || got.SignatureType != st || !bytes.Equal(got.Signature, h.Signature) || got.KeyVersion != 0 {
				t.Fatalf("VerifyCheckpoint = %+v, want %+v without a key version", got, h)
			}
		})
	}
}

func TestRenderCheckpoint_Refusals(t *testing.T) {
	h := SignedHead{Size: 1, Root: []byte{1}, Signature: []byte{2}}
	var hse *HeadSignatureError
	for _, name := range []string{"", "a b", "a+b", "a b", "a\tb", "\xff"} {
		if _, err := RenderCheckpoint("o", h, name, 1); !errors.As(err, &hse) {
			t.Fatalf("key name %q: %v, want *HeadSignatureError", name, err)
		}
	}
	if _, err := RenderCheckpoint("o", SignedHead{Size: 1, Root: []byte{1}}, "k", 1); !errors.As(err, &hse) {
		t.Fatalf("no signature: %v, want *HeadSignatureError", err)
	}
	if _, err := RenderCheckpoint("", h, "k", 1); !errors.As(err, &hse) {
		t.Fatalf("empty origin: %v, want *HeadSignatureError", err)
	}
}

func TestParseCheckpoint_Refusals(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 1, 9})
	line := "— k " + sig + "\n"
	root := base64.StdEncoding.EncodeToString([]byte{1})
	var many strings.Builder
	for i := 0; i <= maxNoteSignatures; i++ {
		many.WriteString("— k" + strings.Repeat("x", i) + " " + sig + "\n")
	}
	cases := map[string]string{
		"not utf-8":          "o\n1\n" + root + "\xff\n\n" + line,
		"control character":  "o\r\n1\n" + root + "\n\n" + line,
		"no blank line":      "o\n1\n" + root + "\n" + line,
		"no signatures":      "o\n1\n" + root + "\n\n",
		"no final newline":   "o\n1\n" + root + "\n\n" + strings.TrimSuffix(line, "\n"),
		"no em dash":         "o\n1\n" + root + "\n\n- k " + sig + "\n",
		"no key name":        "o\n1\n" + root + "\n\n— " + sig + "\n",
		"plus in key name":   "o\n1\n" + root + "\n\n— k+1 " + sig + "\n",
		"bad base64":         "o\n1\n" + root + "\n\n— k AAAA!\n",
		"non-canonical":      "o\n1\n" + root + "\n\n— k AAAAAQl=\n",
		"key hash only":      "o\n1\n" + root + "\n\n— k AAAAAQ==\n",
		"duplicate key":      "o\n1\n" + root + "\n\n" + line + line,
		"too many":           "o\n1\n" + root + "\n\n" + many.String(),
		"two lines":          "o\n1\n\n" + line,
		"empty text line":    "o\n\n1\n" + root + "\n\n" + line,
		"leading zero size":  "o\n01\n" + root + "\n\n" + line,
		"negative size":      "o\n-1\n" + root + "\n\n" + line,
		"bad root":           "o\n1\nAA\n\n" + line,
		"empty root":         "o\n1\n=\n\n" + line,
		"size not a decimal": "o\nx\n" + root + "\n\n" + line,
	}
	for name, note := range cases {
		var hse *HeadSignatureError
		if _, err := ParseCheckpoint([]byte(note)); !errors.As(err, &hse) {
			t.Errorf("%s: %v, want *HeadSignatureError", name, err)
		}
	}
	c, err := ParseCheckpoint([]byte("o\n0\n" + root + "\next\n\n" + line))
	if err != nil || c.Size != 0 || string(c.Text) != "o\n0\n"+root+"\next\n" {
		t.Fatalf("extension line: %+v, %v", c, err)
	}
}

func TestVerifyCheckpoint_Refusals(t *testing.T) {
	p := newDeviceProvider()
	k := deviceKey(t, p, SignatureEd25519, FirstKeyVersion)
	_, note := deviceCheckpoint(t, p, "db.example.com/acme/certs", k)
	pub, err := k.Public()
	if err != nil {
		t.Fatalf("Public: %v", err)
	}
	other := deviceKey(t, p, SignatureEd25519, FirstKeyVersion+1)
	otherPub, err := other.Public()
	if err != nil {
		t.Fatalf("Public: %v", err)
	}
	tampered := bytes.Replace(note, []byte("\n1\n"), []byte("\n2\n"), 1)
	var hse *HeadSignatureError
	for name, call := range map[string]func() error{
		"malformed": func() error {
			_, err := VerifyCheckpoint([]byte("x"), "db.example.com/acme/certs", testLog.String(), pub)
			return err
		},
		"wrong origin": func() error {
			_, err := VerifyCheckpoint(note, "db.example.com/other", testLog.String(), pub)
			return err
		},
		"wrong name": func() error {
			_, err := VerifyCheckpoint(note, "db.example.com/acme/certs", "acme/ledger/other", pub)
			return err
		},
		"unknown key": func() error {
			_, err := VerifyCheckpoint(note, "db.example.com/acme/certs", testLog.String(), otherPub)
			return err
		},
		"tampered text": func() error {
			_, err := VerifyCheckpoint(tampered, "db.example.com/acme/certs", testLog.String(), pub)
			return err
		},
	} {
		if err := call(); !errors.As(err, &hse) {
			t.Errorf("%s: %v, want *HeadSignatureError", name, err)
		}
	}
	var uke *UnsupportedKeyTypeError
	if _, err := VerifyCheckpoint(note, "db.example.com/acme/certs", testLog.String(), "not a key"); !errors.As(err, &uke) {
		t.Fatalf("unsupported key: %v, want *UnsupportedKeyTypeError", err)
	}
}

// TestVerifyCheckpoint_IgnoresUnknownKeys verifies a note that also carries
// a signature line from a key the verifier does not know, as signed-note
// requires.
func TestVerifyCheckpoint_IgnoresUnknownKeys(t *testing.T) {
	p := newDeviceProvider()
	k := deviceKey(t, p, SignatureEd25519, FirstKeyVersion)
	_, note := deviceCheckpoint(t, p, "db.example.com/acme/certs", k)
	pub, err := k.Public()
	if err != nil {
		t.Fatalf("Public: %v", err)
	}
	foreign := "— witness.example.com " + base64.StdEncoding.EncodeToString(append([]byte{1, 2, 3, 4}, make([]byte, 64)...)) + "\n"
	if _, err := VerifyCheckpoint(append(bytes.Clone(note), foreign...), "db.example.com/acme/certs", testLog.String(), pub); err != nil {
		t.Fatalf("VerifyCheckpoint with a cosignature line: %v", err)
	}
}

// TestDescriptor_VerifyCheckpoint_AcrossRotation verifies checkpoints signed
// by either key version under the rotated descriptor, each naming the
// version that signed it.
func TestDescriptor_VerifyCheckpoint_AcrossRotation(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			p := newDeviceProvider()
			desc := testDescriptor(false)
			desc.Keys = []HeadKey{deviceKey(t, p, st, FirstKeyVersion), deviceKey(t, p, st, FirstKeyVersion+1)}
			first, firstNote := deviceCheckpoint(t, p, desc.Origin, desc.Keys[0])
			second, secondNote := deviceCheckpoint(t, p, desc.Origin, desc.Keys[1])
			for _, tc := range []struct {
				note []byte
				want SignedHead
			}{{firstNote, first}, {secondNote, second}} {
				got, err := desc.VerifyCheckpoint(testLog.String(), tc.note)
				if err != nil {
					t.Fatalf("VerifyCheckpoint: %v", err)
				}
				if got.KeyVersion != tc.want.KeyVersion || got.SignatureType != st || !bytes.Equal(got.Signature, tc.want.Signature) {
					t.Fatalf("VerifyCheckpoint = %+v, want %+v", got, tc.want)
				}
			}
			var hse *HeadSignatureError
			if _, err := desc.VerifyCheckpoint("acme/ledger/other", firstNote); !errors.As(err, &hse) {
				t.Fatalf("other key name: %v, want *HeadSignatureError", err)
			}
			unrotated := desc
			unrotated.Keys = desc.Keys[:1]
			if _, err := unrotated.VerifyCheckpoint(testLog.String(), secondNote); !errors.As(err, &hse) {
				t.Fatalf("key the log does not record: %v, want *HeadSignatureError", err)
			}
			moved := desc
			moved.Origin = "db.example.com/other"
			if _, err := moved.VerifyCheckpoint(testLog.String(), firstNote); !errors.As(err, &hse) {
				t.Fatalf("other origin: %v, want *HeadSignatureError", err)
			}
			if _, err := desc.VerifyCheckpoint(testLog.String(), []byte("x")); !errors.As(err, &hse) {
				t.Fatalf("malformed: %v, want *HeadSignatureError", err)
			}
			tampered := bytes.Replace(firstNote, []byte("\n1\n"), []byte("\n2\n"), 1)
			if _, err := desc.VerifyCheckpoint(testLog.String(), tampered); !errors.As(err, &hse) {
				t.Fatalf("tampered: %v, want *HeadSignatureError", err)
			}
			broken := desc
			broken.Keys = []HeadKey{{Version: FirstKeyVersion, Ref: "r", PublicKey: []byte("junk")}}
			if _, err := broken.VerifyCheckpoint(testLog.String(), firstNote); err == nil {
				t.Fatal("a key that does not parse verified")
			}
		})
	}
}
