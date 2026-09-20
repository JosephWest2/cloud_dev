package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/setup"
)

func TestSetupDispatchAndStrictOptions(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		recognized bool
	}{
		{[]string{"setup"}, true}, {[]string{"--config", "setup", "doctor"}, false}, {[]string{"--config", "x", "setup", "--json"}, true}, {[]string{"exec", "i-12345678", "--", "setup"}, false}, {[]string{"--aws-profile=setup", "doctor"}, false},
	} {
		if IsSetupCommand(tc.args) != tc.recognized {
			t.Fatal(tc.args)
		}
	}
	for _, args := range [][]string{
		{"setup", "--yes"}, {"setup", "--config", "a", "--config", "b"}, {"setup", "--resume", "x", "--manifest", "y"}, {"setup", "--timeout", "0s"}, {"setup", "--timeout", "2h"}, {"setup", "--health-timeout", "31m"}, {"setup", "--json=true"}, {"setup", "status", "x", "--aws-profile", "source"}, {"setup", "--region", "us-west-2"}, {"setup", "--manifest", "x", "--bundle", "y"}, {"setup", "unknown"},
	} {
		var out, diag bytes.Buffer
		called := false
		code := runSetupCommand(context.Background(), append(args, "--json"), &out, &diag, func(context.Context, setup.Options, setup.Dependencies) setup.Result {
			called = true
			return setup.Result{}
		})
		var r setup.Result
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(args, err, out.String())
		}
		if code != 2 || r.Code != "setup_invalid" || called {
			t.Fatal(args, code, r, called)
		}
	}
}

func TestSetupOptionsAndOutputOwnership(t *testing.T) {
	var out, diag bytes.Buffer
	code := runSetupCommand(context.Background(), []string{"--aws-profile", "source", "setup", "--bundle", "/bundle", "--timeout", "30m", "--json"}, &out, &diag, func(_ context.Context, o setup.Options, d setup.Dependencies) setup.Result {
		if o.AWSProfile != "source" || o.BundlePath != "/bundle" || o.StageTimeout.String() != "30m0s" {
			t.Fatal(o)
		}
		_, _ = d.Output.Write([]byte("safe progress\n"))
		return setup.Result{SchemaVersion: 1, Command: "setup", OK: true, Completed: []string{}, Checks: []setup.Check{}}
	})
	var r setup.Result
	if json.Unmarshal(out.Bytes(), &r) != nil || code != 0 || diag.String() != "safe progress\n" {
		t.Fatal(out.String(), diag.String(), code)
	}
}
