package execprotocol

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

var literalArgs = []string{"", "two words", "line\nfeed", "'\"", "$(touch SHOULD_NOT_EXIST); & | > * ?", "こんにちは🙂", "--json", "--", "-leading", `\ud800`}

func TestLiteralPayloadRoundTrip(t *testing.T) {
	argv := append([]string{"printf"}, literalArgs...)
	p, err := NewPayload(argv, "", 3600)
	if err != nil || p.Cwd != DefaultCwd {
		t.Fatalf("constructor: %+v %v", p, err)
	}
	encoded, err := EncodePayload(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePayload(encoded)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	argv[0] = "changed"
	if p.Argv[0] != "printf" {
		t.Fatal("constructor did not copy argv")
	}
}

func TestPayloadRejection(t *testing.T) {
	for _, modify := range []func(*Payload){
		func(p *Payload) { p.SchemaVersion = 0 },
		func(p *Payload) { p.Argv = nil },
		func(p *Payload) { p.Argv[0] = "" },
		func(p *Payload) { p.Argv = append(p.Argv, strings.Repeat("x", MaxEncodedPayload)) },
		func(p *Payload) { p.Argv = append(p.Argv, make([]string, MaxArguments)...) },
		func(p *Payload) { p.Argv = append(p.Argv, "bad\x00arg") },
		func(p *Payload) { p.Argv = append(p.Argv, "bad\xffarg") },
		func(p *Payload) { p.Cwd = "relative" },
		func(p *Payload) { p.Cwd = "/bad\xff" },
		func(p *Payload) { p.Cwd = "/bad\x00" },
		func(p *Payload) { p.ExecTimeoutSeconds = 0 },
		func(p *Payload) { p.ExecTimeoutSeconds = MaxExecutionSeconds + 1 },
	} {
		p, _ := NewPayload([]string{"program"}, "", 1)
		modify(&p)
		if _, err := EncodePayload(p); err == nil {
			t.Fatalf("accepted invalid payload %+v", p)
		}
	}
	base := `{"schema_version":1,"argv":["program"],"cwd":"/home/devbox","exec_timeout_seconds":1}`
	for _, raw := range []string{
		base + ` {}`, strings.Replace(base, `"schema_version":1,`, "", 1),
		strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(base, `"argv"`, `"ARGV"`, 1), strings.Replace(base, `"cwd"`, `"extra"`, 1),
		strings.Replace(base, `"program"`, `"\ud800"`, 1), strings.Replace(base, `"program"`, `"\udfff"`, 1),
		strings.Replace(base, `"program"`, `"\u0000"`, 1), strings.Replace(base, "program", "\xff", 1),
		strings.Replace(base, `"argv":["program"]`, `"argv":null`, 1),
		strings.Replace(base, `"argv":["program"]`, `"argv":["program",null]`, 1),
		strings.Replace(base, `"exec_timeout_seconds":1`, `"exec_timeout_seconds":1.5`, 1),
	} {
		if _, err := DecodePayload(base64.StdEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatalf("accepted invalid JSON %q", raw)
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(base))
	for _, raw := range []string{"", "!!!", encoded + "\n", strings.Repeat("A", MaxEncodedPayload+4)} {
		if _, err := DecodePayload(raw); err == nil {
			t.Fatal("accepted invalid base64 or size")
		}
	}
	paired := strings.Replace(base, "program", `\ud83d\ude42`, 1)
	if p, err := DecodePayload(base64.StdEncoding.EncodeToString([]byte(paired))); err != nil || p.Argv[0] != "🙂" {
		t.Fatalf("valid surrogate pair: %+v %v", p, err)
	}
}

func TestPayloadSizeBoundary(t *testing.T) {
	p, _ := NewPayload([]string{"p", ""}, "", MaxExecutionSeconds)
	encoded, _ := EncodePayload(p)
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	p.Argv[1] = strings.Repeat("x", MaxEncodedPayload/4*3-len(raw))
	encoded, err := EncodePayload(p)
	if err != nil || len(encoded) != MaxEncodedPayload {
		t.Fatalf("exact bound rejected: %d %v", len(encoded), err)
	}
	if _, err := DecodePayload(encoded); err != nil {
		t.Fatal(err)
	}
	p.Argv[1] += "x"
	if _, err := EncodePayload(p); err == nil {
		t.Fatal("oversized payload accepted")
	}
}
