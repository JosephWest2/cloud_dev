package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

// LaunchPlan is the complete immutable input to a batch, before attempt identity
// and the count of a particular attempt are attached. Constructing it is pure;
// the allocator must additionally verify the deployed foundation before sending.
type LaunchPlan struct {
	SchemaVersion      int                   `json:"schema_version"`
	Account            string                `json:"account"`
	Region             string                `json:"region"`
	Deployment         string                `json:"deployment"`
	Owner              string                `json:"owner"`
	RequestID          string                `json:"request_id"`
	CreatedAt          string                `json:"created_at"`
	Profile            string                `json:"profile"`
	BaseName           string                `json:"base_name"`
	Group              string                `json:"group,omitempty"`
	Market             string                `json:"market"`
	RequestedCount     int                   `json:"requested_count"`
	Choices            []config.LaunchChoice `json:"choices"`
	Image              config.Image          `json:"image"`
	RootDisk           config.RootDisk       `json:"root_disk"`
	SecurityGroupID    string                `json:"security_group_id"`
	InstanceProfileARN string                `json:"instance_profile_arn"`
	BootstrapSHA256    string                `json:"bootstrap_sha256"`
	Readiness          config.Document       `json:"readiness"`
	Execution          config.Execution      `json:"execution"`
	LaunchLedger       config.LaunchLedger   `json:"launch_ledger"`
	CreationTags       map[string]string     `json:"creation_tags"`
}

// BuildLaunchPlan validates a new request without making any cloud calls. The
// returned choices are only the profile's intersection with the manifest's
// approved offerings, never a guessed cartesian product or capacity promise.
func BuildLaunchPlan(c config.Config, m config.Manifest, p config.Profile, s LaunchSelection, requestID, createdAt string) (LaunchPlan, error) {
	var plan LaunchPlan
	var err error
	p, err = config.NormalizeProfile(p)
	if err != nil {
		return plan, failure("profile_invalid", err.Error())
	}
	if !ValidRequest(requestID) {
		return plan, failure("request_invalid", "a batch requires a 32-character request identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return plan, failure("request_invalid", "a batch requires an exact creation timestamp")
	}
	limit := c.MaxCount
	if limit == 0 {
		limit = config.DefaultMaxCount
	}
	if limit < 1 || limit > config.HardMaxCount || s.Count < 1 || s.Count > limit {
		return plan, failure("count_invalid", "requested instance count exceeds the configured max_count or supported range")
	}
	if s.Profile != "agent" || s.Profile != p.Name || !ValidName(s.Name) || (s.Group != "" && !ValidGroup(s.Group)) || s.Resume != "" || s.RetryMissing != "" || s.After != "" {
		return plan, failure("launch_invalid", "a new batch requires valid profile, base name and group without recovery options")
	}
	if c.ExpectedAccount != m.Account || c.Region != m.Region || c.Deployment != m.Deployment || c.Owner != m.Owner {
		return plan, failure("scope_mismatch", "launch configuration and manifest scope differ")
	}
	choices, err := config.ValidateProfileManifest(p, m)
	if err != nil {
		return plan, failure("launch_invalid", err.Error())
	}
	market := p.Market
	if s.OnDemand {
		market = "on-demand"
	} else if market == "on-demand" {
		return plan, failure("market_opt_in_required", "On-Demand requires explicit --on-demand, including when selected by a custom profile")
	}
	// Preserve profile order for On-Demand: every worker uses its first type.
	// Spot retains every approved choice for price-capacity-optimized allocation.
	if market == "on-demand" {
		filtered := make([]config.LaunchChoice, 0, len(choices))
		for _, choice := range choices {
			if choice.InstanceType == p.InstanceTypes[0] {
				filtered = append(filtered, choice)
			}
		}
		choices = filtered
	}
	if len(choices) == 0 {
		return plan, failure("launch_invalid", "no approved type and subnet combinations remain")
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].InstanceType != choices[j].InstanceType {
			return choices[i].InstanceType < choices[j].InstanceType
		}
		return choices[i].SubnetID < choices[j].SubnetID
	})
	plan = LaunchPlan{
		SchemaVersion: 1, Account: c.ExpectedAccount, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner,
		RequestID: requestID, CreatedAt: createdAt, Profile: p.Name, BaseName: s.Name, Group: s.Group,
		Market: market, RequestedCount: s.Count, Choices: choices, Image: m.Images[p.Image],
		RootDisk:        config.RootDisk{SizeGB: p.DiskGB, Type: p.DiskType, Encrypted: p.Encrypted, DeleteOnTermination: p.DeleteOnTermination},
		SecurityGroupID: m.SecurityGroupID, InstanceProfileARN: m.InstanceProfileARN,
		BootstrapSHA256: m.BootstrapSHA256, Readiness: m.Readiness, Execution: m.Execution,
		LaunchLedger: *m.LaunchLedger,
		CreationTags: map[string]string{"ManagedBy": "devbox", "Deployment": c.Deployment, "Owner": c.Owner,
			"Profile": p.Name, "Name": s.Name, "BaseName": s.Name, "RequestId": requestID,
			"BatchId": requestID, "CreatedAt": createdAt, "NamingVersion": "1"},
	}
	if s.Group != "" {
		plan.CreationTags["Group"] = s.Group
	}
	// Copy the pointer-bearing image so a caller cannot mutate the template's
	// recorded disk defaults by changing the original manifest afterward.
	if plan.Image.RootDisk != nil {
		disk := *plan.Image.RootDisk
		plan.Image.RootDisk = &disk
	}
	return plan, nil
}

// Digest pins the canonical JSON representation (including ordered choices).
func (p LaunchPlan) Digest() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// WorkerName never depends on response order, ordinal allocation or local state.
// Legacy workers still use their unmodified Name tag; this rule is opt-in via
// NamingVersion=1 and BaseName on new workers at creation.
func WorkerName(base, instanceID string) (string, error) {
	if !ValidName(base) || !instanceRE.MatchString(instanceID) {
		return "", failure("name_invalid", "worker naming requires a valid base and exact instance ID")
	}
	suffix := "-" + instanceID
	return base[:min(len(base), 63-len(suffix))] + suffix, nil
}

// AttemptID identifies the only successor of a parent. Neither count, current
// time nor mutable receipt contents affect its slot in the request's lineage.
func AttemptID(requestID, parentID string) (string, error) {
	if !ValidRequest(requestID) || (parentID != "" && !ValidRequest(parentID)) {
		return "", failure("request_invalid", "attempt identity requires valid request and parent identities")
	}
	h := sha256.Sum256([]byte("devbox-attempt-v2\x00" + requestID + "\x00" + parentID))
	return hex.EncodeToString(h[:16]), nil
}

// AttemptTags returns an isolated creation-time tag map shared by every
// instance, root volume and fleet in this attempt. A unique per-instance Name
// cannot be expressed by Fleet-wide TagSpecifications.
func (p LaunchPlan) AttemptTags(attemptID string) (map[string]string, error) {
	if !ValidRequest(attemptID) {
		return nil, failure("request_invalid", "attempt requires a valid identity")
	}
	tags := make(map[string]string, len(p.CreationTags)+1)
	for k, v := range p.CreationTags {
		tags[k] = v
	}
	tags["AttemptId"] = attemptID
	return tags, nil
}

const ExitPartial = 3

type BatchResult struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	ExitCode      int    `json:"exit_code"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	BatchOutcome
}

// BatchOutcome is separate from allocation completeness: all capacity can be
// allocated while individual workers fail readiness. Known IDs survive either.
type BatchOutcome struct {
	RequestID      string           `json:"request_id"`
	Status         string           `json:"status"`
	RequestedCount int              `json:"requested_count"`
	FulfilledCount int              `json:"fulfilled_count"`
	MissingCount   *int             `json:"missing_count"` // null if allocation is unknown
	ReadyCount     int              `json:"ready_count"`
	Plan           LaunchPlan       `json:"plan"`
	Attempts       []AttemptOutcome `json:"attempts"`
	Workers        []WorkerOutcome  `json:"instances"`
	Errors         []ResourceError  `json:"errors"`
	ResumeCommand  string           `json:"resume_command,omitempty"`
	RetryCommand   string           `json:"retry_command,omitempty"`
}

type AttemptOutcome struct {
	AttemptID      string          `json:"attempt_id"`
	ParentID       string          `json:"parent_attempt_id,omitempty"`
	FleetID        string          `json:"fleet_id,omitempty"`
	Status         string          `json:"status"`
	RequestedCount int             `json:"requested_count"`
	InstanceIDs    []string        `json:"instance_ids"`
	MissingCount   *int            `json:"missing_count"`
	Errors         []ResourceError `json:"errors"`
}

type WorkerOutcome struct {
	Instance
	Group            string `json:"group,omitempty"`
	BaseName         string `json:"base_name"`
	AttemptID        string `json:"attempt_id"`
	SubnetID         string `json:"subnet_id"`
	AvailabilityZone string `json:"availability_zone"`
	Status           string `json:"status"`
}

type ResourceError struct {
	ResourceID   string `json:"resource_id,omitempty"`
	InstanceType string `json:"instance_type,omitempty"`
	SubnetID     string `json:"subnet_id,omitempty"`
	Code         string `json:"code"`
	Message      string `json:"message"`
}

// Preview describes eligible choices, not promised placements or market prices.
func (p LaunchPlan) Preview() string {
	choices := make([]string, 0, len(p.Choices))
	for _, c := range p.Choices {
		choices = append(choices, c.InstanceType+"/"+c.SubnetID+"/"+c.AvailabilityZone)
	}
	return fmt.Sprintf("profile=%s region=%s count=%d market=%s base=%s group=%s eligible=%s", p.Profile, p.Region, p.RequestedCount, p.Market, p.BaseName, p.Group, strings.Join(choices, ","))
}
