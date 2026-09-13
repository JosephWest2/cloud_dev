// Package sshkey validates the deliberately small Ed25519 access contract.
package sshkey

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

func Parse(s string) ([]byte, error) {
	parts := strings.Split(s, " ")
	if len(parts) != 2 || parts[0] != "ssh-ed25519" {
		return nil, errors.New("expected one Ed25519 public key without a comment")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(b) != 51 || binary.BigEndian.Uint32(b[:4]) != 11 || !bytes.Equal(b[4:15], []byte("ssh-ed25519")) || binary.BigEndian.Uint32(b[15:19]) != 32 {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return b, nil
}

func Fingerprint(s string) string {
	b, err := Parse(s)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(b)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
}
