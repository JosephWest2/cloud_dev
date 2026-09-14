package lifecycle

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCount(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		maximum   int
		want      int
	}{
		{name: "default", want: 1},
		{name: "one", raw: "1", want: 1},
		{name: "two", raw: "2", want: 2},
		{name: "decimal leading zeros", raw: "002", want: 2},
		{name: "default maximum", raw: "10", want: 10},
		{name: "configured maximum", raw: "100", maximum: 100, want: 100},
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "signed", raw: "+1"},
		{name: "fractional", raw: "1.5"},
		{name: "scientific", raw: "1e1"},
		{name: "spaces", raw: " 2"},
		{name: "unicode digits", raw: "２"},
		{name: "line break", raw: "2\n"},
		{name: "default exceeded", raw: "11"},
		{name: "configured exceeded", raw: "3", maximum: 2},
		{name: "hard limit exceeded", raw: "101", maximum: 100},
		{name: "overflow", raw: strings.Repeat("9", 100)},
		{name: "invalid maximum", raw: "1", maximum: -1},
		{name: "excessive maximum", raw: "1", maximum: 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCount(tc.raw, tc.maximum)
			if got != tc.want || (err != nil) != (tc.want == 0) {
				t.Fatalf("ParseCount(%q, %d) = %d, %v; want %d", tc.raw, tc.maximum, got, err, tc.want)
			}
		})
	}
}

func TestResolveLaunchSelection(t *testing.T) {
	request := strings.Repeat("a", 32)
	attempt := strings.Repeat("b", 32)
	for _, tc := range []struct {
		name       string
		positional []string
		flags      LaunchFlags
		want       LaunchSelection
	}{
		{name: "parent example", positional: []string{"agent"}, flags: LaunchFlags{Count: "2", Group: "smoke-batch"}, want: LaunchSelection{Profile: "agent", Count: 2, Name: "smoke-batch", Group: "smoke-batch"}},
		{name: "profile defaults", positional: []string{"agent"}, want: LaunchSelection{Profile: "agent", Count: 1, Name: "agent"}},
		{name: "explicit base", positional: []string{"agent"}, flags: LaunchFlags{Group: "smoke", Name: "worker", Count: "2", OnDemand: true}, want: LaunchSelection{Profile: "agent", Count: 2, Name: "worker", Group: "smoke", OnDemand: true}},
		{name: "legacy single", positional: []string{"agent"}, flags: LaunchFlags{Name: "worker", OnDemand: true}, want: LaunchSelection{Profile: "agent", Count: 1, Name: "worker", OnDemand: true}},
		{name: "maximum labels", positional: []string{"agent"}, flags: LaunchFlags{Name: strings.Repeat("a", 63), Group: strings.Repeat("b", 63)}, want: LaunchSelection{Profile: "agent", Count: 1, Name: strings.Repeat("a", 63), Group: strings.Repeat("b", 63)}},
		{name: "resume", flags: LaunchFlags{Resume: request}, want: LaunchSelection{Resume: request}},
		{name: "retry missing", flags: LaunchFlags{RetryMissing: request, After: attempt}, want: LaunchSelection{RetryMissing: request, After: attempt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLaunchSelection(tc.positional, tc.flags, 0)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestResolveLaunchSelectionRejectsInvalid(t *testing.T) {
	request, attempt := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, tc := range []struct {
		name       string
		positional []string
		flags      LaunchFlags
	}{
		{name: "no profile"},
		{name: "unknown profile", positional: []string{"other"}},
		{name: "extra positional", positional: []string{"agent", "extra"}},
		{name: "large name", positional: []string{"agent"}, flags: LaunchFlags{Name: strings.Repeat("a", 64)}},
		{name: "large group", positional: []string{"agent"}, flags: LaunchFlags{Group: strings.Repeat("a", 64)}},
		{name: "name instance ID", positional: []string{"agent"}, flags: LaunchFlags{Name: "i-12345678"}},
		{name: "group instance ID", positional: []string{"agent"}, flags: LaunchFlags{Group: "i-12345678"}},
		{name: "name first character", positional: []string{"agent"}, flags: LaunchFlags{Name: "_worker"}},
		{name: "group punctuation", positional: []string{"agent"}, flags: LaunchFlags{Group: "smoke/batch"}},
		{name: "group whitespace", positional: []string{"agent"}, flags: LaunchFlags{Group: "smoke batch"}},
		{name: "excessive count", positional: []string{"agent"}, flags: LaunchFlags{Count: "11"}},
		{name: "resume invalid ID", flags: LaunchFlags{Resume: "request"}},
		{name: "resume uppercase ID", flags: LaunchFlags{Resume: strings.Repeat("A", 32)}},
		{name: "resume profile", positional: []string{"agent"}, flags: LaunchFlags{Resume: request}},
		{name: "resume count", flags: LaunchFlags{Resume: request, Count: "1"}},
		{name: "resume name", flags: LaunchFlags{Resume: request, Name: "worker"}},
		{name: "resume group", flags: LaunchFlags{Resume: request, Group: "smoke"}},
		{name: "resume market", flags: LaunchFlags{Resume: request, OnDemand: true}},
		{name: "resume after", flags: LaunchFlags{Resume: request, After: attempt}},
		{name: "conflicting recovery", flags: LaunchFlags{Resume: request, RetryMissing: request, After: attempt}},
		{name: "retry missing after", flags: LaunchFlags{RetryMissing: request}},
		{name: "retry invalid request", flags: LaunchFlags{RetryMissing: "invalid", After: attempt}},
		{name: "retry invalid attempt", flags: LaunchFlags{RetryMissing: request, After: "invalid"}},
		{name: "retry profile", positional: []string{"agent"}, flags: LaunchFlags{RetryMissing: request, After: attempt}},
		{name: "retry count", flags: LaunchFlags{RetryMissing: request, After: attempt, Count: "1"}},
		{name: "retry name", flags: LaunchFlags{RetryMissing: request, After: attempt, Name: "worker"}},
		{name: "retry group", flags: LaunchFlags{RetryMissing: request, After: attempt, Group: "smoke"}},
		{name: "retry market", flags: LaunchFlags{RetryMissing: request, After: attempt, OnDemand: true}},
		{name: "after alone", positional: []string{"agent"}, flags: LaunchFlags{After: attempt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveLaunchSelection(tc.positional, tc.flags, 0); err == nil {
				t.Fatal("accepted invalid selection")
			}
		})
	}
}

func TestValidateDownSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection DownSelection
		valid     bool
	}{
		{name: "name", selection: DownSelection{Targets: []string{"smoke"}}, valid: true},
		{name: "ID", selection: DownSelection{Targets: []string{"i-12345678"}}, valid: true},
		{name: "multiple", selection: DownSelection{Targets: []string{"smoke", "i-1234567890abcdef0"}}, valid: true},
		{name: "group", selection: DownSelection{Group: "smoke"}, valid: true},
		{name: "all prompt", selection: DownSelection{All: true}, valid: true},
		{name: "all yes", selection: DownSelection{All: true, Yes: true}, valid: true},
		{name: "no selector"},
		{name: "name with group", selection: DownSelection{Targets: []string{"smoke"}, Group: "smoke"}},
		{name: "name with all", selection: DownSelection{Targets: []string{"smoke"}, All: true}},
		{name: "group with all", selection: DownSelection{Group: "smoke", All: true}},
		{name: "yes alone", selection: DownSelection{Yes: true}},
		{name: "yes target", selection: DownSelection{Targets: []string{"smoke"}, Yes: true}},
		{name: "yes group", selection: DownSelection{Group: "smoke", Yes: true}},
		{name: "invalid target", selection: DownSelection{Targets: []string{"smoke", "bad/target"}}},
		{name: "invalid group", selection: DownSelection{Group: "bad/group"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateDownSelection(tc.selection); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, got %v", tc.valid, err)
			}
		})
	}
}
