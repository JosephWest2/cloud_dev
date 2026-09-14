package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/sshkey"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	st "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type SSM interface {
	GetDocument(context.Context, *ssm.GetDocumentInput, ...func(*ssm.Options)) (*ssm.GetDocumentOutput, error)
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
	StartSession(context.Context, *ssm.StartSessionInput, ...func(*ssm.Options)) (*ssm.StartSessionOutput, error)
	TerminateSession(context.Context, *ssm.TerminateSessionInput, ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error)
}

// Resolve does not trust EC2 filters, including for explicit IDs. Retain matches
// on ambiguity for recovery, but never act on an ambiguous selection.
func (s *Service) Resolve(ctx context.Context, target string) ([]Instance, error) {
	if !ValidTarget(target) {
		return nil, failure("target_invalid", "use a friendly name or EC2 instance ID")
	}
	id, name := "", target
	if instanceRE.MatchString(target) {
		id, name = target, ""
	}
	found, err := s.inventory(ctx, id, name, "")
	if err != nil {
		return found, err
	}
	found = active(found)
	if len(found) == 0 {
		return found, failure("target_unresolved", "no live managed target; run devbox ls in the same scope")
	}
	if len(found) != 1 {
		return found, failure("name_ambiguous", "multiple managed instances share this name; use an explicit instance ID from devbox ls")
	}
	return found, nil
}

func (s *Service) VerifyProbe(ctx context.Context, m config.Manifest) error {
	if s.SSM == nil {
		return failure("probe_unavailable", "SSM client unavailable; reinstall devbox")
	}
	out, err := s.SSM.GetDocument(ctx, &ssm.GetDocumentInput{Name: aws.String(m.Readiness.Name), DocumentVersion: aws.String(m.Readiness.Version), DocumentFormat: st.DocumentFormatJson})
	if err != nil {
		return probeError(err)
	}
	if out == nil || aws.ToString(out.Name) != m.Readiness.Name || aws.ToString(out.DocumentVersion) != m.Readiness.Version || out.DocumentType != st.DocumentTypeCommand || out.Status != st.DocumentStatusActive {
		return failure("probe_invalid", "pinned readiness document unavailable; apply and re-export the foundation")
	}
	var doc any
	if json.Unmarshal([]byte(aws.ToString(out.Content)), &doc) != nil {
		return failure("probe_invalid", "readiness document is invalid; re-export the foundation")
	}
	canonical, _ := json.Marshal(doc)
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != m.Readiness.ContentSHA256 {
		return failure("probe_changed", "readiness document differs from the trusted export; review and re-export the foundation")
	}
	return nil
}

func probeError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if apiCode(err, "AccessDeniedException") || apiCode(err, "AccessDenied") {
		return failure("probe_denied", "SSM observation denied; check DescribeInstanceInformation, pinned GetDocument/SendCommand and GetCommandInvocation permissions")
	}
	return failure("probe_unavailable", "SSM observation unavailable; check credentials, connectivity and the pinned readiness document; retry devbox ls")
}

func observationError(i *Instance, err error) {
	if i.SSM == "" || i.SSM == "not_observed" {
		i.SSM = "unknown"
	}
	if i.Bootstrap == "" || i.Bootstrap == "not_observed" {
		i.Bootstrap = "unknown"
	}
	i.Readiness = "unknown"
	i.ObservationCode = "probe_unavailable"
	var f *Failure
	if errors.As(err, &f) {
		i.ObservationCode = f.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		i.ObservationCode = "observation_timeout"
	}
}

func (s *Service) observe(ctx context.Context, m config.Manifest, i *Instance) error {
	return s.observeWithGuard(ctx, m, i, nil)
}

// Batch callers additionally bind their immutable launch pins at the final
// scope lookup immediately before a probe is sent. Legacy callers retain their
// existing exact-target and scope checks without requiring a batch receipt.
func (s *Service) observeWithGuard(ctx context.Context, m config.Manifest, i *Instance, guard func(Instance) error) (err error) {
	i.SSM, i.Bootstrap, i.Readiness, i.ObservationCode, i.HostKey = "unknown", "unknown", "unknown", "", ""
	defer func() {
		if err != nil {
			observationError(i, err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	info, e := s.SSM.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{Filters: []st.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{i.ID}}}})
	if e != nil {
		return probeError(e)
	}
	if info == nil || aws.ToString(info.NextToken) != "" || len(info.InstanceInformationList) > 1 {
		return failure("probe_invalid", "SSM returned an invalid target observation; retry inspection")
	}
	i.SSM = "not_registered"
	if len(info.InstanceInformationList) == 1 {
		node := info.InstanceInformationList[0]
		if aws.ToString(node.InstanceId) != i.ID {
			return failure("probe_invalid", "SSM returned a different target; no probe sent")
		}
		switch node.PingStatus {
		case st.PingStatusOnline:
			i.SSM = "online"
		case st.PingStatusConnectionLost, st.PingStatusInactive:
			i.SSM = "offline"
		default:
			return failure("probe_invalid", "SSM returned an unknown connectivity state")
		}
	}
	i.Readiness = "pending"
	if i.State == "terminated" || i.State == "shutting-down" || i.State == "stopped" || i.State == "stopping" {
		i.Readiness = "not_ready"
		return nil
	}
	if i.SSM != "online" || i.State != "running" {
		return nil
	}
	// A command is only dispatched after a fresh EC2 scope check. No user
	// parameters, output destinations, or arbitrary command documents are allowed.
	fresh, e := s.Resolve(ctx, i.ID)
	if e != nil {
		return e
	}
	if guard != nil {
		if e = guard(fresh[0]); e != nil {
			return e
		}
	}
	if fresh[0].State != "running" {
		i.State = fresh[0].State
		i.Readiness = "not_ready"
		return nil
	}
	out, e := s.SSM.SendCommand(ctx, &ssm.SendCommandInput{InstanceIds: []string{i.ID}, DocumentName: aws.String(m.Readiness.Name), DocumentVersion: aws.String(m.Readiness.Version), TimeoutSeconds: aws.Int32(30)})
	if e != nil {
		return probeError(e)
	}
	if out == nil || out.Command == nil || !commandRE.MatchString(aws.ToString(out.Command.CommandId)) {
		return failure("probe_invalid", "SSM did not return a recoverable probe command ID; retry inspection")
	}
	i.ProbeCommandID = aws.ToString(out.Command.CommandId)
	for attempt := 0; ; attempt++ {
		if e = ctx.Err(); e != nil {
			return e
		}
		invocation, e := s.SSM.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{CommandId: aws.String(i.ProbeCommandID), InstanceId: aws.String(i.ID), PluginName: aws.String("bootstrapStatus")})
		if apiCode(e, "InvocationDoesNotExist") {
			if e = s.pause(ctx, attempt); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return probeError(e)
		}
		if invocation == nil || aws.ToString(invocation.InstanceId) != i.ID || aws.ToString(invocation.CommandId) != i.ProbeCommandID || aws.ToString(invocation.DocumentName) != m.Readiness.Name || aws.ToString(invocation.DocumentVersion) != m.Readiness.Version || aws.ToString(invocation.PluginName) != "bootstrapStatus" {
			return failure("probe_invalid", "SSM invocation identity does not match the pinned probe")
		}
		switch invocation.Status {
		case st.CommandInvocationStatusPending, st.CommandInvocationStatusInProgress, st.CommandInvocationStatusDelayed:
			if e = s.pause(ctx, attempt); e != nil {
				return e
			}
			continue
		case st.CommandInvocationStatusSuccess:
			if invocation.ResponseCode != 0 || aws.ToString(invocation.StandardErrorContent) != "" {
				return failure("probe_failed", "readiness probe execution failed; bootstrap state is unknown; inspect the command ID")
			}
		default:
			return failure("probe_failed", "readiness probe did not succeed; bootstrap state is unknown; inspect the command ID")
		}
		var signal struct {
			SchemaVersion int    `json:"schema_version"`
			Bootstrap     string `json:"bootstrap"`
			HostKey       string `json:"host_key"`
		}
		output := aws.ToString(invocation.StandardOutputContent)
		if len(output) > 1024 {
			return failure("probe_invalid", "readiness probe output exceeds its fixed contract")
		}
		d := json.NewDecoder(bytes.NewBufferString(output))
		d.DisallowUnknownFields()
		if d.Decode(&signal) != nil || d.Decode(new(any)) != io.EOF || signal.SchemaVersion != 1 {
			return failure("probe_invalid", "readiness probe output is malformed; apply and re-export the current foundation")
		}
		switch signal.Bootstrap {
		case "complete":
			if _, e = sshkey.Parse(signal.HostKey); e != nil {
				return failure("probe_invalid", "completed bootstrap did not supply a valid Ed25519 host key")
			}
			i.HostKey, i.Readiness = signal.HostKey, "ready"
		case "pending":
			if signal.HostKey != "" {
				return failure("probe_invalid", "pending bootstrap returned unexpected host trust data")
			}
		case "failed":
			if signal.HostKey != "" {
				return failure("probe_invalid", "failed bootstrap returned unexpected host trust data")
			}
			i.Readiness = "failed"
		default:
			return failure("probe_invalid", "readiness probe returned an unknown bootstrap marker")
		}
		i.Bootstrap = signal.Bootstrap
		return nil
	}
}

// ObserveAll preserves every inventory record and bounds concurrency and the
// entire operation, rather than spending a full deadline on each instance.
func (s *Service) ObserveAll(ctx context.Context, m config.Manifest, instances []Instance) error {
	if err := s.VerifyProbe(ctx, m); err != nil {
		for n := range instances {
			instances[n].SSM, instances[n].Bootstrap = "unknown", "unknown"
			observationError(&instances[n], err)
		}
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	errs := make([]error, len(instances))
	for n := range instances {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				errs[n] = s.observe(ctx, m, &instances[n])
			case <-ctx.Done():
				errs[n] = ctx.Err()
				observationError(&instances[n], ctx.Err())
			}
		}(n)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) WaitReady(ctx context.Context, m config.Manifest, i *Instance, progress io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := s.VerifyProbe(ctx, m); err != nil {
		observationError(i, err)
		return err
	}
	last := ""
	for attempt := 0; ; attempt++ {
		fresh, err := s.Resolve(ctx, i.ID)
		if err != nil {
			observationError(i, err)
			return err
		}
		i.State = fresh[0].State
		err = s.observe(ctx, m, i)
		state := fmt.Sprintf("ec2=%s ssm=%s bootstrap=%s readiness=%s", i.State, i.SSM, i.Bootstrap, i.Readiness)
		if state != last && progress != nil {
			fmt.Fprintf(progress, "devbox: %s %s\n", i.ID, state)
			last = state
		}
		if err != nil {
			return err
		}
		if i.Readiness == "ready" {
			return nil
		}
		if i.Readiness == "failed" {
			return failure("bootstrap_failed", "bootstrap reported failure; retain the probe command ID for inspection; remove the instance with devbox down INSTANCE_ID after investigation")
		}
		if i.Readiness == "not_ready" {
			return failure("instance_not_running", "instance cannot become ready in its current EC2 state; inspect or tear down by instance ID")
		}
		if err = s.pause(ctx, attempt); err != nil {
			return err
		}
	}
}
