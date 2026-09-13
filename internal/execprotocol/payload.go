// Package execprotocol defines the versioned literal-argument execution payload.
// Its Linux tests also contain a deliberately local publisher prototype: separate
// processes and a file-backed fake object store demonstrate observer independence.
// The prototype does not implement S3, SSM, user switching, production environment
// isolation, cancellation, timeouts, retention, or crash-proof cloud publication.
package execprotocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion       = 1
	DefaultCwd          = "/home/devbox"
	MaxArguments        = 256
	MaxEncodedPayload   = 32768
	MaxExecutionSeconds = 86400
)

type Payload struct {
	SchemaVersion      int      `json:"schema_version"`
	Argv               []string `json:"argv"`
	Cwd                string   `json:"cwd"`
	ExecTimeoutSeconds int      `json:"exec_timeout_seconds"`
}

// NewPayload supplies the documented working directory when cwd is empty.
// Arguments are copied and never joined into shell source.
func NewPayload(argv []string, cwd string, timeoutSeconds int) (Payload, error) {
	if cwd == "" {
		cwd = DefaultCwd
	}
	p := Payload{SchemaVersion, append([]string(nil), argv...), cwd, timeoutSeconds}
	return p, p.Validate()
}

// Validate rejects unsupported bytes before encoding/json could replace them.
// Encoded size is checked separately by EncodePayload and DecodePayload.
func (p Payload) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return errors.New("unsupported execution payload schema")
	}
	if len(p.Argv) == 0 || len(p.Argv) > MaxArguments || p.Argv[0] == "" {
		return errors.New("execution requires 1–256 arguments and a nonempty executable")
	}
	if p.ExecTimeoutSeconds < 1 || p.ExecTimeoutSeconds > MaxExecutionSeconds {
		return errors.New("execution timeout must be 1–86400 seconds")
	}
	if !path.IsAbs(p.Cwd) {
		return errors.New("execution working directory must be absolute")
	}
	for _, value := range append([]string{p.Cwd}, p.Argv...) {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("execution arguments and directory must be valid UTF-8 without NUL")
		}
	}
	return nil
}

func EncodePayload(p Payload) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", errors.New("cannot encode execution payload")
	}
	if base64.StdEncoding.EncodedLen(len(b)) > MaxEncodedPayload {
		return "", errors.New("encoded execution payload exceeds 32768 bytes")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// DecodePayload accepts one canonical base64 value containing an exact JSON
// object. Unknown, duplicate, case-mismatched, and missing fields are rejected.
func DecodePayload(encoded string) (Payload, error) {
	var p Payload
	bad := errors.New("invalid execution payload encoding or fields")
	if len(encoded) == 0 || len(encoded) > MaxEncodedPayload {
		return p, bad
	}
	b, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(b) != encoded || !utf8.Valid(b) || !validEscapedUnicode(b) {
		return p, bad
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return p, bad
	}
	// Pointers distinguish a JSON null element from the valid empty argument.
	var argv []*string
	fields := map[string]any{"schema_version": &p.SchemaVersion, "argv": &argv, "cwd": &p.Cwd, "exec_timeout_seconds": &p.ExecTimeoutSeconds}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return Payload{}, bad
		}
		name, ok := key.(string)
		target, known := fields[name]
		if !ok || !known || d.Decode(target) != nil {
			return Payload{}, bad
		}
		delete(fields, name)
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') || len(fields) != 0 {
		return Payload{}, bad
	}
	if d.Decode(new(any)) != io.EOF {
		return Payload{}, bad
	}
	for _, arg := range argv {
		if arg == nil {
			return Payload{}, bad
		}
		p.Argv = append(p.Argv, *arg)
	}
	if err := p.Validate(); err != nil {
		return Payload{}, err
	}
	return p, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject them so
// decoding cannot silently alter an argument. JSON syntax is checked afterward.
func validEscapedUnicode(b []byte) bool {
	hex := func(start int) (uint16, bool) {
		if start+4 > len(b) {
			return 0, false
		}
		var n uint16
		for _, c := range b[start : start+4] {
			n <<= 4
			switch {
			case c >= '0' && c <= '9':
				n += uint16(c - '0')
			case c >= 'a' && c <= 'f':
				n += uint16(c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				n += uint16(c - 'A' + 10)
			default:
				return 0, false
			}
		}
		return n, true
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) || b[i] != 'u' {
			continue
		}
		n, ok := hex(i + 1)
		if !ok {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, ok := hex(i + 3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
