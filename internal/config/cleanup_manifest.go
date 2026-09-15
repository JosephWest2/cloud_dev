package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
)

// Cleanup is decoded separately from Manifest so scheduler drift or malformed
// optional capability data cannot disable inventory, teardown or saved results.
type Cleanup struct {
	SchemaVersion int             `json:"schema_version"`
	Function      CleanupFunction `json:"function"`
	Schedule      CleanupSchedule `json:"schedule"`
	Logs          CleanupLogs     `json:"logs"`
	ExecutionRole Role            `json:"execution_role"`
	SchedulerRole Role            `json:"scheduler_role"`
	Evidence      json.RawMessage `json:"evidence,omitempty"`
}
type CleanupFunction struct {
	ARN                   string `json:"arn"`
	Runtime               string `json:"runtime"`
	Architecture          string `json:"architecture"`
	CodeSHA256            string `json:"code_sha256"`
	EnvironmentSHA256     string `json:"environment_sha256"`
	TimeoutSeconds        int    `json:"timeout_seconds"`
	ServiceTimeoutSeconds int    `json:"service_timeout_seconds"`
	ReservedConcurrency   int    `json:"reserved_concurrency"`
	MemoryMB              int    `json:"memory_mb"`
	AsyncMaxAgeSeconds    int    `json:"async_max_age_seconds"`
	AsyncRetryAttempts    int    `json:"async_retry_attempts"`
}
type CleanupSchedule struct {
	ARN            string `json:"arn"`
	Name           string `json:"name"`
	GroupName      string `json:"group_name"`
	GroupARN       string `json:"group_arn"`
	State          string `json:"state"`
	Expression     string `json:"expression"`
	FlexibleWindow string `json:"flexible_window"`
	MaxAgeSeconds  int    `json:"max_age_seconds"`
	RetryAttempts  int    `json:"retry_attempts"`
	InputSHA256    string `json:"input_sha256"`
}
type CleanupLogs struct {
	Name          string `json:"name"`
	ARN           string `json:"arn"`
	RetentionDays int    `json:"retention_days"`
}

func DecodeCleanup(raw json.RawMessage, m Manifest) (Cleanup, error) {
	var c Cleanup
	fail := errors.New("cleanup descriptor is absent or invalid; review the v6 foundation export independently of manual recovery")
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, fail
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fail
	}
	// Zero is a required explicit budget, not an omitted/default configuration.
	var envelope map[string]json.RawMessage
	var function map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope["function"], &function) != nil || string(function["async_retry_attempts"]) != "0" {
		return c, fail
	}
	name := "devbox-" + m.Deployment + "-" + m.Owner
	regional := m.Region + ":" + m.Account
	f, s, l := c.Function, c.Schedule, c.Logs
	if m.SchemaVersion != 6 || c.SchemaVersion != 1 || f.ARN != "arn:aws:lambda:"+regional+":function:"+name+"-cleanup" || f.Runtime != "provided.al2023" || f.Architecture != "x86_64" || !digestRE.MatchString(f.CodeSHA256) || !digestRE.MatchString(f.EnvironmentSHA256) || f.TimeoutSeconds != 180 || f.ServiceTimeoutSeconds != 165 || f.ReservedConcurrency != 1 || f.MemoryMB != 256 || f.AsyncMaxAgeSeconds != 300 || f.AsyncRetryAttempts != 0 {
		return c, fail
	}
	if s.Name != name+"-cleanup" || s.GroupName != s.Name || s.ARN != "arn:aws:scheduler:"+regional+":schedule/"+s.GroupName+"/"+s.Name || s.GroupARN != "arn:aws:scheduler:"+regional+":schedule-group/"+s.GroupName || !slices.Contains([]string{"ENABLED", "DISABLED"}, s.State) || s.Expression != "rate(5 minutes)" || s.FlexibleWindow != "OFF" || s.MaxAgeSeconds != 300 || s.RetryAttempts != 2 || !digestRE.MatchString(s.InputSHA256) {
		return c, fail
	}
	if l.Name != "/aws/lambda/"+name+"-cleanup" || l.ARN != "arn:aws:logs:"+regional+":log-group:"+l.Name || !slices.Contains([]int{7, 14, 30, 60, 90, 120, 150, 180, 365}, l.RetentionDays) {
		return c, fail
	}
	for suffix, r := range map[string]Role{"cleanup": c.ExecutionRole, "schedule": c.SchedulerRole} {
		if r.ARN != "arn:aws:iam::"+m.Account+":role/"+name+"-"+suffix || r.PolicyName != "devbox-"+suffix || !digestRE.MatchString(r.TrustSHA256) || !digestRE.MatchString(r.PolicySHA256) {
			return c, fail
		}
	}
	if len(c.Evidence) > 0 {
		var capability struct {
			SchemaVersion int `json:"schema_version"`
		}
		if json.Unmarshal(c.Evidence, &capability) != nil || capability.SchemaVersion != 1 {
			return c, fail
		}
	}
	return c, nil
}
