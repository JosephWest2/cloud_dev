package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/doctor"
)

func TestVersionOutputs(t *testing.T) {
	previous := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = previous })
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"version"}, "devbox 1.2.3\n"},
		{[]string{"--json", "version"}, "{\"schema_version\":1,\"command\":\"version\",\"ok\":true,\"exit_code\":0,\"version\":\"1.2.3\"}\n"},
	} {
		var out, diag bytes.Buffer
		if code := Run(context.Background(), tc.args, &out, &diag, doctor.Dependencies{}); code != 0 || out.String() != tc.want || diag.Len() != 0 {
			t.Fatalf("version: exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
		}
	}
}
