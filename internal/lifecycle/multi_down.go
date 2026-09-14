package lifecycle

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const teardownConcurrency = 4
const teardownDeadline = 5 * time.Minute
const teardownRequestDeadline = 15 * time.Second
const teardownInventoryPages = 128

type TeardownWorker struct {
	Instance
	Status string          `json:"status"`
	Errors []ResourceError `json:"errors"`
}

type TeardownOutcome struct {
	Status          string           `json:"status"`
	SelectedCount   int              `json:"selected_count"`
	TerminatedCount int              `json:"terminated_count"`
	CleanedCount    int              `json:"cleaned_count"`
	Workers         []TeardownWorker `json:"instances"`
	Errors          []ResourceError  `json:"errors"`
}

type downCandidate struct {
	instance Instance
	names    []string
	group    string
}

// TeardownPlan is an in-memory selection, bound to the verified service scope.
// Its private candidates cannot be expanded by modifying preview/output data.
// Confirmation is the caller's responsibility; selection never mutates AWS.
type TeardownPlan struct {
	valid                              bool
	account, region, deployment, owner string
	candidates                         []downCandidate
	outcome                            TeardownOutcome
}

func copyDownInstance(i Instance) Instance {
	i.Volumes = append([]Volume{}, i.Volumes...)
	return i
}

func (p TeardownPlan) Candidates() []Instance {
	result := make([]Instance, 0, len(p.candidates))
	for _, candidate := range p.candidates {
		result = append(result, copyDownInstance(candidate.instance))
	}
	return result
}

func (p TeardownPlan) Outcome() TeardownOutcome {
	result := p.outcome
	result.Errors = append([]ResourceError{}, result.Errors...)
	result.Workers = append([]TeardownWorker{}, result.Workers...)
	for n := range result.Workers {
		result.Workers[n].Instance = copyDownInstance(result.Workers[n].Instance)
		result.Workers[n].Errors = append([]ResourceError{}, result.Workers[n].Errors...)
	}
	return result
}

// Each lookup has a bounded page count and each individual AWS request has a
// short deadline. A stalled worker occupies at most one of four execution slots.
type downInventoryAPI struct {
	EC2
	remaining int
}

func (a *downInventoryAPI) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if a.remaining == 0 {
		return nil, failure("inventory_unavailable", "inventory exceeded the teardown page bound; narrow the selection and retry")
	}
	a.remaining--
	requestCtx, cancel := context.WithTimeout(ctx, teardownRequestDeadline)
	defer cancel()
	return a.EC2.DescribeInstances(requestCtx, in, opts...)
}

func (s *Service) downInventory(ctx context.Context, id, name, group string) ([]Instance, error) {
	bounded := *s
	bounded.API = &downInventoryAPI{EC2: s.API, remaining: teardownInventoryPages}
	return bounded.inventorySelection(ctx, id, name, "", group)
}

func downResourceError(id string, err error) ResourceError {
	result := ResourceError{ResourceID: id, Code: "teardown_unverified", Message: "cleanup could not be verified; inspect the returned instance and volume IDs and retry down by ID"}
	var known *Failure
	if errors.As(err, &known) {
		result.Code, result.Message = known.Code, known.Message
	} else if errors.Is(err, context.Canceled) {
		result.Code, result.Message = "interrupted", "cleanup observation was interrupted; inspect the returned instance and volume IDs and retry down by ID"
	} else if errors.Is(err, context.DeadlineExceeded) {
		result.Code, result.Message = "timeout", "cleanup observation reached its deadline; inspect the returned instance and volume IDs and retry down by ID"
	}
	return result
}

// SelectDown resolves the entire selector before any termination. An incomplete
// group/all scan authorizes no IDs. Independent explicit selectors may succeed
// alongside resolution errors; an ambiguous name authorizes none of its matches.
func (s *Service) SelectDown(ctx context.Context, selection DownSelection) (TeardownPlan, error) {
	p := TeardownPlan{account: s.Scope.ExpectedAccount, region: s.Scope.Region, deployment: s.Scope.Deployment, owner: s.Scope.Owner,
		outcome: TeardownOutcome{Status: "teardown_failed", Workers: []TeardownWorker{}, Errors: []ResourceError{}}}
	if err := ValidateDownSelection(selection); err != nil {
		return p, failure("selection_invalid", "use explicit worker names/IDs, one group, or all scoped workers; selectors cannot be combined")
	}
	ctx, cancel := context.WithTimeout(ctx, teardownDeadline)
	defer cancel()
	p.valid = true
	workers, candidates := map[string]int{}, map[string]int{}
	addWorker := func(i Instance, status string, resourceErr *ResourceError) {
		position, exists := workers[i.ID]
		if !exists {
			position = len(p.outcome.Workers)
			workers[i.ID] = position
			p.outcome.Workers = append(p.outcome.Workers, TeardownWorker{Instance: copyDownInstance(i), Status: status, Errors: []ResourceError{}})
		} else {
			p.outcome.Workers[position].Volumes = mergeFleetVolumes(p.outcome.Workers[position].Volumes, i.Volumes)
		}
		if resourceErr != nil {
			item := *resourceErr
			item.ResourceID = i.ID
			p.outcome.Workers[position].Errors = append(p.outcome.Workers[position].Errors, item)
		}
	}
	addCandidate := func(i Instance, name, group string) {
		addWorker(i, "termination_not_requested", nil)
		position, exists := candidates[i.ID]
		if !exists {
			position = len(p.candidates)
			candidates[i.ID] = position
			p.candidates = append(p.candidates, downCandidate{instance: copyDownInstance(i), group: group})
		} else {
			p.candidates[position].instance.Volumes = mergeFleetVolumes(p.candidates[position].instance.Volumes, i.Volumes)
		}
		if name != "" {
			p.candidates[position].names = append(p.candidates[position].names, name)
		}
	}
	finish := func() {
		sort.Slice(p.candidates, func(i, j int) bool { return p.candidates[i].instance.ID < p.candidates[j].instance.ID })
		sort.Slice(p.outcome.Workers, func(i, j int) bool { return p.outcome.Workers[i].ID < p.outcome.Workers[j].ID })
		p.outcome.SelectedCount = len(p.candidates)
		if len(p.candidates) != 0 {
			p.outcome.Status = "selected"
		} else if len(p.outcome.Errors) == 0 {
			p.outcome.Status = "no_managed_match"
		}
	}
	if selection.All || selection.Group != "" {
		found, err := s.downInventory(ctx, "", "", selection.Group)
		if err != nil {
			item := downResourceError(selection.Group, err)
			p.outcome.Errors = append(p.outcome.Errors, item)
			for _, i := range found {
				addWorker(i, "selection_unverified", &item)
			}
			finish()
			return p, err
		}
		for _, i := range found {
			addCandidate(i, "", selection.Group)
		}
		finish()
		return p, nil
	}
	seenTargets := map[string]bool{}
	for _, target := range selection.Targets {
		if seenTargets[target] {
			continue
		}
		seenTargets[target] = true
		id, name := "", target
		if instanceRE.MatchString(target) {
			id, name = target, ""
		}
		found, err := s.downInventory(ctx, id, name, "")
		if err == nil {
			// Preserve harmless historical cleanup when no live match exists. A
			// friendly name is still ambiguous if several historical IDs remain.
			if live := active(found); len(live) != 0 {
				found = live
			}
			switch len(found) {
			case 0:
				err = failure("target_unresolved", "no exact managed target was visible; absence does not verify cleanup; inspect inventory in the selected scope")
			case 1:
				addCandidate(found[0], name, "")
			default:
				err = failure("name_ambiguous", "multiple managed workers share this name; inspect the returned candidate IDs and retry with an explicit ID")
			}
		}
		if err != nil {
			item := downResourceError(target, err)
			p.outcome.Errors = append(p.outcome.Errors, item)
			for _, i := range found {
				addWorker(i, "selection_unverified", &item)
			}
			if len(found) == 0 && id != "" {
				addWorker(Instance{ID: id, State: "unknown", RootDeletion: "unavailable", Volumes: []Volume{}}, "target_unresolved", &item)
			}
		}
	}
	finish()
	if ctx.Err() != nil {
		// Selection interrupted before all explicit targets were resolved is
		// observation only; there is no partially implied authorization.
		p.candidates = nil
		p.outcome.SelectedCount, p.outcome.Status = 0, "teardown_failed"
		return p, ctx.Err()
	}
	return p, nil
}

func mergeDownObservation(known, fresh Instance) Instance {
	fresh.Volumes = mergeFleetVolumes(known.Volumes, fresh.Volumes)
	fresh.RootDeletion = "unavailable"
	for _, volume := range fresh.Volumes {
		if volume.Root {
			fresh.RootDeletion = volume.Deletion
		}
	}
	return fresh
}

// ExecuteDown consumes only the frozen IDs. No name/group scan is performed
// after confirmation; the exact-ID read below is the final read before mutation.
func (s *Service) ExecuteDown(ctx context.Context, p TeardownPlan) (TeardownOutcome, error) {
	result := p.Outcome()
	if !p.valid || p.account != s.Scope.ExpectedAccount || p.region != s.Scope.Region || p.deployment != s.Scope.Deployment || p.owner != s.Scope.Owner {
		err := failure("scope_mismatch", "teardown selection does not match the current verified scope; select the targets again")
		result.Status = "teardown_failed"
		result.Errors = append(result.Errors, downResourceError("", err))
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, teardownDeadline)
	defer cancel()
	positions := map[string]int{}
	for n, worker := range result.Workers {
		positions[worker.ID] = n
	}
	jobs := make(chan downCandidate)
	var wait sync.WaitGroup
	for n := 0; n < min(teardownConcurrency, len(p.candidates)); n++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for candidate := range jobs {
				position := positions[candidate.instance.ID]
				worker := s.executeDownWorker(ctx, candidate)
				worker.Errors = append(result.Workers[position].Errors, worker.Errors...)
				result.Workers[position] = worker
			}
		}()
	}
	for _, candidate := range p.candidates {
		jobs <- candidate
	}
	close(jobs)
	wait.Wait()
	for _, worker := range result.Workers {
		result.Errors = append(result.Errors, worker.Errors...)
		if worker.Status == "termination_observed" || worker.Status == "already_terminated" {
			result.TerminatedCount++
			if downRootsDeleted(worker.Instance) {
				result.CleanedCount++
			}
		}
	}
	switch {
	case result.SelectedCount == 0 && len(result.Errors) == 0:
		result.Status = "no_managed_match"
	case result.CleanedCount == result.SelectedCount && result.SelectedCount != 0 && len(result.Errors) == 0:
		result.Status = "teardown_complete"
	case result.CleanedCount != 0:
		result.Status = "teardown_partial"
	default:
		result.Status = "teardown_failed"
	}
	return result, ctx.Err()
}

func downRootsDeleted(i Instance) bool {
	found := false
	for _, volume := range i.Volumes {
		if volume.Root {
			found = true
			if volume.Deletion != "deleted" {
				return false
			}
		}
	}
	return found
}

func (s *Service) executeDownWorker(ctx context.Context, candidate downCandidate) TeardownWorker {
	worker := TeardownWorker{Instance: copyDownInstance(candidate.instance), Status: "termination_not_requested", Errors: []ResourceError{}}
	fail := func(err error) TeardownWorker {
		worker.Errors = append(worker.Errors, downResourceError(worker.ID, err))
		return worker
	}
	current, err := s.downInventory(ctx, worker.ID, "", "")
	for _, observed := range current {
		if observed.ID == worker.ID {
			worker.Volumes = mergeFleetVolumes(worker.Volumes, observed.Volumes)
		}
	}
	if err != nil {
		return fail(err)
	}
	if len(current) != 1 {
		return fail(failure("target_unresolved", "selected instance is no longer visible; termination and root deletion remain unverified; inspect the returned IDs"))
	}
	worker.Instance = mergeDownObservation(worker.Instance, current[0])
	for _, name := range candidate.names {
		if worker.Name != name {
			return fail(failure("scope_mismatch", "selected worker name changed before termination; inspect inventory before selecting the target again"))
		}
	}
	if candidate.group != "" && worker.Group != candidate.group {
		return fail(failure("scope_mismatch", "selected worker left the selected group before termination; inspect inventory before selecting the target again"))
	}
	if worker.State == "terminated" {
		worker.Status = "already_terminated"
		worker.Errors = append(worker.Errors, s.observeDownVolumes(ctx, &worker.Instance)...)
		return worker
	}
	switch worker.State {
	case "pending", "running", "stopping", "stopped", "shutting-down":
	default:
		return fail(failure("state_unknown", "instance state is unavailable; inspect the returned ID before retrying cleanup"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	worker.Status = "termination_requested"
	if worker.State != "shutting-down" {
		requestCtx, cancel := context.WithTimeout(ctx, teardownRequestDeadline)
		_, err = s.API.TerminateInstances(requestCtx, &ec2.TerminateInstancesInput{InstanceIds: []string{worker.ID}})
		cancel()
		if apiCode(err, "UnauthorizedOperation") || apiCode(err, "AuthFailure") || apiCode(err, "AccessDenied") || apiCode(err, "AccessDeniedException") {
			worker.Status = "termination_denied"
			return fail(failure("termination_denied", "EC2 denied termination; refresh credentials and check scoped termination permission for the returned instance ID"))
		}
		if apiCode(err, "OperationNotPermitted") || apiCode(err, "Client.OperationNotPermitted") {
			worker.Status = "termination_denied"
			return fail(failure("termination_protected", "EC2 refused termination; inspect termination protection and the returned instance ID before retrying"))
		}
		if err != nil {
			// A lost or malformed response can follow an accepted request. The
			// exact terminal observation below is the only completion proof.
			worker.Status = "termination_unknown"
		}
	}
	for attempt := 0; attempt < 30; attempt++ {
		current, err = s.downInventory(ctx, worker.ID, "", "")
		for _, observed := range current {
			if observed.ID == worker.ID {
				worker.Volumes = mergeFleetVolumes(worker.Volumes, observed.Volumes)
			}
		}
		if err != nil {
			return fail(err)
		}
		if len(current) == 1 {
			worker.Instance = mergeDownObservation(worker.Instance, current[0])
			if worker.State == "terminated" {
				worker.Status = "termination_observed"
				worker.Errors = append(worker.Errors, s.observeDownVolumes(ctx, &worker.Instance)...)
				return worker
			}
		}
		if err = s.pause(ctx, attempt); err != nil {
			return fail(err)
		}
	}
	return fail(failure("termination_unresolved", "termination was not observed within the retry bound; inspect the returned instance and volume IDs and retry down by ID"))
}

// Volume deletion is observed only after an exact terminal instance observation.
// An empty successful DescribeVolumes response is not deletion proof. A known
// root with DeleteOnTermination=false is retained and never explicitly deleted.
func (s *Service) observeDownVolumes(ctx context.Context, instance *Instance) []ResourceError {
	result := []ResourceError{}
	rootFound := false
	for n := range instance.Volumes {
		volume := &instance.Volumes[n]
		rootFound = rootFound || volume.Root
		if !volume.DeleteOnTermination {
			volume.Deletion = "retained"
			if volume.Root {
				result = append(result, downResourceError(volume.ID, failure("root_volume_retained", "the root volume is retained; inspect its exact ID and delete it separately only if intended")))
			}
			continue
		}
		deleted := false
		var observationErr error
		for attempt := 0; attempt < 30; attempt++ {
			if observationErr = ctx.Err(); observationErr != nil {
				break
			}
			requestCtx, cancel := context.WithTimeout(ctx, teardownRequestDeadline)
			out, err := s.API.DescribeVolumes(requestCtx, &ec2.DescribeVolumesInput{VolumeIds: []string{volume.ID}})
			cancel()
			if apiCode(err, "InvalidVolume.NotFound") && out == nil {
				deleted = true
				break
			}
			if err != nil || !s.exactDownVolume(out, volume.ID) {
				observationErr = failure("volume_unresolved", "EC2 terminated but exact volume deletion could not be verified; inspect the returned volume ID with DescribeVolumes")
				break
			}
			if out.Volumes[0].State == types.VolumeStateDeleted {
				deleted = true
				break
			}
			if observationErr = s.pause(ctx, attempt); observationErr != nil {
				break
			}
		}
		if deleted {
			volume.Deletion = "deleted"
		} else {
			volume.Deletion = "unavailable"
			if observationErr == nil {
				observationErr = failure("volume_unresolved", "EC2 terminated but volume deletion was not observed within the retry bound; inspect the returned volume ID")
			}
			result = append(result, downResourceError(volume.ID, observationErr))
		}
	}
	instance.RootDeletion = "unavailable"
	if !rootFound {
		result = append(result, downResourceError(instance.ID, failure("root_volume_unverified", "EC2 terminated but no root-volume mapping is available; inspect prior output and exact volume IDs before claiming cleanup")))
	} else if downRootsDeleted(*instance) {
		instance.RootDeletion = "deleted"
	} else {
		for _, volume := range instance.Volumes {
			if volume.Root && volume.Deletion == "retained" {
				instance.RootDeletion = "retained"
			}
		}
	}
	return result
}

func (s *Service) exactDownVolume(out *ec2.DescribeVolumesOutput, id string) bool {
	if out == nil || aws.ToString(out.NextToken) != "" || len(out.Volumes) != 1 || aws.ToString(out.Volumes[0].VolumeId) != id {
		return false
	}
	volume := out.Volumes[0]
	if volume.OwnerId != nil && aws.ToString(volume.OwnerId) != s.Scope.ExpectedAccount {
		return false
	}
	if volume.VolumeArn != nil && aws.ToString(volume.VolumeArn) != "arn:aws:ec2:"+s.Scope.Region+":"+s.Scope.ExpectedAccount+":volume/"+id {
		return false
	}
	if volume.AvailabilityZone != nil && (!validInventoryZone(aws.ToString(volume.AvailabilityZone)) || !strings.HasPrefix(aws.ToString(volume.AvailabilityZone), s.Scope.Region)) {
		return false
	}
	if volume.State == types.VolumeStateDeleted {
		for _, attachment := range volume.Attachments {
			if attachment.State != types.VolumeAttachmentStateDetached {
				return false
			}
		}
	}
	return true
}
