package lifecycle

import (
	"errors"
	"strconv"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
)

// LaunchFlags records command-line intent before defaults are applied. Empty
// strings mean omitted options; the CLI rejects explicit empty values.
type LaunchFlags struct {
	Count, Name, Group          string
	Resume, RetryMissing, After string
	OnDemand                    bool
	TTL                         *string
}

// LaunchSelection is a pure, validated selection, not authorization to allocate.
// Recovery selections carry no profile, name, group, count or market override.
type LaunchSelection struct {
	Profile, Name, Group        string
	Count                       int
	OnDemand                    bool
	TTL                         *string
	Resume, RetryMissing, After string
}

// ParseCount accepts only decimal digits and counts instances, not vCPUs. A zero
// maximum means the configured default. An omitted count means one instance.
func ParseCount(raw string, maximum int) (int, error) {
	if maximum == 0 {
		maximum = config.DefaultMaxCount
	}
	if maximum < 1 || maximum > config.HardMaxCount {
		return 0, errors.New("maximum count must be between 1 and 100 instances")
	}
	if raw == "" {
		return 1, nil
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, errors.New("--count must be a positive decimal integer within the effective maximum")
		}
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > maximum {
		return 0, errors.New("--count must be a positive decimal integer within the effective maximum")
	}
	return count, nil
}

// ValidGroup uses the same friendly-label contract as launch names, so a group
// can also supply the default name base without changing validation rules.
func ValidGroup(group string) bool { return ValidName(group) }

func ResolveLaunchSelection(positional []string, flags LaunchFlags, maximum int) (LaunchSelection, error) {
	var selection LaunchSelection
	if flags.Resume != "" || flags.RetryMissing != "" {
		if len(positional) != 0 || flags.Count != "" || flags.Name != "" || flags.Group != "" || flags.OnDemand || flags.TTL != nil || flags.Resume != "" && flags.RetryMissing != "" {
			return selection, failure("replay_override", "recovery rejects all explicit launch overrides, including --ttl; create a new request to change its lifetime")
		}
		if flags.Resume != "" {
			if !ValidRequest(flags.Resume) || flags.After != "" {
				return selection, errors.New("use up --resume REQUEST_ID without other recovery or launch parameters; IDs are 32 lowercase hexadecimal digits")
			}
			selection.Resume = flags.Resume
			return selection, nil
		}
		if !ValidRequest(flags.RetryMissing) || !ValidRequest(flags.After) {
			return selection, errors.New("use up --retry-missing REQUEST_ID --after ATTEMPT_ID; both IDs are 32 lowercase hexadecimal digits")
		}
		selection.RetryMissing, selection.After = flags.RetryMissing, flags.After
		return selection, nil
	}
	if flags.TTL != nil {
		if _, err := expiry.ParseTTL(*flags.TTL); err != nil {
			return selection, expiryFailure(err)
		}
	}
	if flags.After != "" {
		return selection, errors.New("--after requires --retry-missing REQUEST_ID")
	}
	if len(positional) != 1 || positional[0] != "agent" {
		return selection, errors.New("use up agent [--count N] [--group GROUP] [--name BASE] [--on-demand]")
	}
	if flags.Group != "" && !ValidGroup(flags.Group) {
		return selection, errors.New("--group must be a friendly label of 1–63 letters, digits, underscores or hyphens, starting with a letter or digit, and must not look like an instance ID")
	}
	name := flags.Name
	if name == "" {
		name = flags.Group
		if name == "" {
			name = positional[0]
		}
	}
	if !ValidName(name) {
		return selection, errors.New("--name must be a friendly label of 1–63 letters, digits, underscores or hyphens, starting with a letter or digit, and must not look like an instance ID")
	}
	count, err := ParseCount(flags.Count, maximum)
	if err != nil {
		return selection, err
	}
	return LaunchSelection{Profile: positional[0], Name: name, Group: flags.Group, Count: count, OnDemand: flags.OnDemand, TTL: flags.TTL}, nil
}

// DownSelection selects either explicit names/IDs, one group, or the full
// managed scope. All always requires a preview; Yes only skips its prompt.
type DownSelection struct {
	Targets []string
	Group   string
	All     bool
	Yes     bool
}

func ValidateDownSelection(selection DownSelection) error {
	selectors := 0
	if len(selection.Targets) != 0 {
		selectors++
	}
	if selection.Group != "" {
		selectors++
	}
	if selection.All {
		selectors++
	}
	if selectors != 1 {
		return errors.New("use down NAME_OR_INSTANCE_ID [...] or down --group GROUP or down --all; selectors cannot be combined")
	}
	if selection.Yes && !selection.All {
		return errors.New("--yes is only valid with down --all")
	}
	if selection.Group != "" && !ValidGroup(selection.Group) {
		return errors.New("--group requires a valid friendly label of 1–63 characters")
	}
	for _, target := range selection.Targets {
		if !ValidTarget(target) {
			return errors.New("down targets must each be a valid friendly name or EC2 instance ID")
		}
	}
	return nil
}
