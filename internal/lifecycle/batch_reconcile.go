package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type BatchInventory interface {
	FleetInventory
	DescribeFleets(context.Context, *ec2.DescribeFleetsInput, ...func(*ec2.Options)) (*ec2.DescribeFleetsOutput, error)
}

type LaunchObservation struct {
	Receipt BatchReceipt
	Workers []WorkerOutcome
	Errors  []ResourceError
	Bounded bool
	// HistoricalFulfillment binds IDs to attempts using validated original
	// shared responses, independently of contradictory current observations.
	HistoricalFulfillment map[string]string
}

type launchReconciler struct {
	api        BatchInventory
	snapshot   LaunchSnapshot
	out        LaunchObservation
	attempts   map[string]int
	workers    map[string]WorkerOutcome
	historical map[string]bool
}

// ReconcileLaunch is observation-only. Even an empty, fully paginated inventory
// cannot supply the original allocation bound that an immutable response lacks.
func ReconcileLaunch(ctx context.Context, api BatchInventory, snapshot LaunchSnapshot) (LaunchObservation, error) {
	r := &launchReconciler{api: api, snapshot: snapshot, out: LaunchObservation{Receipt: cloneBatch(snapshot.Receipt), Workers: []WorkerOutcome{}, Errors: []ResourceError{}, Bounded: true}, attempts: map[string]int{}, workers: map[string]WorkerOutcome{}, historical: map[string]bool{}}
	for _, worker := range snapshotWorkers(snapshot) {
		r.workers[worker.ID] = worker
	}
	if err := r.validate(); err != nil {
		r.out.Bounded = false
		return r.result(), err
	}
	if api == nil {
		r.problem("", "launch_inventory_unavailable", "EC2 inspection is unavailable; preserve this request and resume without allocating.")
	} else {
		r.scan(ctx)
		for _, attempt := range snapshot.Receipt.Attempts {
			if attempt.FleetID != "" {
				r.fleet(ctx, attempt)
			}
		}
		r.verify(ctx)
	}
	result := r.result()
	if ctx.Err() != nil {
		result.Bounded = false
		return result, ctx.Err()
	}
	if len(result.Errors) > 0 {
		return result, failure("launch_reconciliation_unavailable", "Launch inspection is incomplete or contradictory; preserve every known identity and resume without allocating.")
	}
	return result, nil
}

func (r *launchReconciler) result() LaunchObservation {
	r.out.HistoricalFulfillment = map[string]string{}
	for _, attempt := range r.snapshot.Receipt.Attempts {
		for _, id := range attempt.InstanceIDs {
			if r.historical[id] {
				r.out.HistoricalFulfillment[id] = attempt.AttemptID
			}
		}
	}
	r.out.Workers = make([]WorkerOutcome, 0, len(r.workers))
	for _, worker := range r.workers {
		r.out.Workers = append(r.out.Workers, worker)
	}
	sort.Slice(r.out.Workers, func(i, j int) bool { return r.out.Workers[i].ID < r.out.Workers[j].ID })
	return r.out
}

func (r *launchReconciler) problem(id, code, message string) {
	r.out.Bounded = false
	if !instanceRE.MatchString(id) && !fleetIDRE.MatchString(id) {
		id = ""
	}
	for _, previous := range r.out.Errors {
		if previous.ResourceID == id && previous.Code == code {
			return
		}
	}
	r.out.Errors = append(r.out.Errors, ResourceError{ResourceID: id, Code: code, Message: message})
}

func (r *launchReconciler) validate() error {
	if r.snapshot.Receipt.Validate() != nil || len(r.snapshot.Prepared) != len(r.snapshot.Receipt.Attempts) {
		return ErrLaunchLedgerCorrupt
	}
	prior := cloneBatch(r.snapshot.Receipt)
	prior.Attempts = []AttemptReceipt{}
	for n, attempt := range r.snapshot.Receipt.Attempts {
		r.attempts[attempt.AttemptID] = n
		prepared, ok := r.snapshot.Prepared[attempt.AttemptID]
		if !ok || !validPrepared(prior, prepared) {
			return ErrLaunchLedgerCorrupt
		}
		claim, claimed := r.snapshot.Claims[attempt.AttemptID]
		if claimed && claim != expectedClaim(prepared) {
			return ErrLaunchLedgerCorrupt
		}
		response, responded := r.snapshot.Responses[attempt.AttemptID]
		if responded && (!claimed || !validResponse(prior, prepared, response)) {
			return ErrLaunchLedgerCorrupt
		}
		if responded && (response.Attempt.State == "complete" || response.Attempt.State == "rejected") {
			if !reflect.DeepEqual(attempt, response.Attempt) {
				return ErrLaunchLedgerCorrupt
			}
			for _, id := range response.Attempt.InstanceIDs {
				r.historical[id] = true
			}
			prior.Attempts = append(prior.Attempts, response.Attempt)
		} else {
			if attempt.State == "complete" || attempt.State == "rejected" || (!claimed && attempt.State != "prepared") {
				return ErrLaunchLedgerCorrupt
			}
			r.out.Bounded = false
			prior.Attempts = append(prior.Attempts, attempt)
		}
	}
	for id := range r.snapshot.Claims {
		if _, exists := r.attempts[id]; !exists {
			return ErrLaunchLedgerCorrupt
		}
	}
	for id := range r.snapshot.Responses {
		if _, exists := r.attempts[id]; !exists {
			return ErrLaunchLedgerCorrupt
		}
	}
	return nil
}

func (r *launchReconciler) retain(attemptID, id string, choice config.LaunchChoice, mappings []Volume) {
	if !instanceRE.MatchString(id) {
		r.problem("", "launch_inventory_invalid", "EC2 returned an invalid worker identity; inspect the original request.")
		return
	}
	n, knownAttempt := r.attempts[attemptID]
	worker, exists := r.workers[id]
	if !exists {
		if knownAttempt {
			worker = fleetWorker(r.out.Receipt.Plan, r.out.Receipt.Attempts[n], id, choice)
		} else {
			worker = WorkerOutcome{Instance: Instance{ID: id, Volumes: []Volume{}}, Status: "identity_mismatch"}
		}
	} else if knownAttempt && worker.AttemptID != attemptID {
		r.problem(id, "launch_identity_mismatch", "A worker was reported under conflicting attempt identities; inspect before requesting more capacity.")
	}
	worker.Volumes = mergeFleetVolumes(worker.Volumes, mappings)
	if choice != (config.LaunchChoice{}) {
		if (worker.Type != "" && worker.Type != choice.InstanceType) || (worker.SubnetID != "" && worker.SubnetID != choice.SubnetID) || (worker.AvailabilityZone != "" && worker.AvailabilityZone != choice.AvailabilityZone) {
			r.problem(id, "launch_identity_mismatch", "A worker's reported placement differs from its immutable response.")
		} else {
			worker.Type, worker.SubnetID, worker.AvailabilityZone = choice.InstanceType, choice.SubnetID, choice.AvailabilityZone
		}
	}
	r.workers[id] = worker
	if !knownAttempt {
		r.problem(id, "launch_identity_mismatch", "EC2 returned a worker with an unknown attempt identity; preserve it for inspection.")
		return
	}
	if _, claimed := r.snapshot.Claims[attemptID]; !claimed {
		r.problem(id, "launch_identity_mismatch", "EC2 reported a worker for an attempt without shared dispatch evidence.")
	}
	attempt := &r.out.Receipt.Attempts[n]
	found := false
	for _, known := range attempt.InstanceIDs {
		found = found || known == id
	}
	if !found {
		if attempt.State == "complete" || attempt.State == "rejected" {
			r.problem(id, "launch_response_conflict", "EC2 reported a worker absent from the original complete response; further allocation is blocked.")
		}
		attempt.State = "unknown"
		attempt.InstanceIDs = unionInstanceIDs(attempt.InstanceIDs, []string{id})
	}
}

func (r *launchReconciler) scan(ctx context.Context) {
	p := r.out.Receipt.Plan
	input := &ec2.DescribeInstancesInput{Filters: []types.Filter{
		{Name: aws.String("tag:ManagedBy"), Values: []string{"devbox"}},
		{Name: aws.String("tag:Deployment"), Values: []string{p.Deployment}},
		{Name: aws.String("tag:Owner"), Values: []string{p.Owner}},
		{Name: aws.String("tag:RequestId"), Values: []string{p.RequestID}},
	}}
	tokens, ids := map[string]bool{}, map[string]bool{}
	for ctx.Err() == nil {
		out, err := r.api.DescribeInstances(ctx, input)
		if out != nil {
			for _, reservation := range out.Reservations {
				for _, instance := range reservation.Instances {
					actual, tags := record(instance), tagsOf(instance)
					id, attemptID := actual.ID, tags["AttemptId"]
					r.retain(attemptID, id, config.LaunchChoice{}, actual.Volumes)
					n, known := r.attempts[attemptID]
					valid := false
					if known {
						_, valid = fleetObservedTags(instance.Tags, p, r.out.Receipt.Attempts[n])
						worker := r.workers[id]
						if r.historical[id] && (actual.State == "terminated" || actual.State == "shutting-down") {
							valid = valid && historicalInstanceMatches(p, r.out.Receipt.Attempts[n], worker, aws.ToString(reservation.OwnerId), instance)
						} else {
							observation := &fleetWorkerObservation{worker: worker}
							observeFleetInstance(observation, p, r.out.Receipt.Attempts[n], aws.ToString(reservation.OwnerId), instance)
							valid = valid && !observation.mismatch
						}
					}
					if !valid || aws.ToString(reservation.OwnerId) != p.Account || ids[id] {
						r.problem(id, "launch_identity_mismatch", "Scoped EC2 inventory returned a duplicate or mismatched worker identity.")
					}
					ids[id] = true
				}
			}
		}
		if err != nil || out == nil {
			r.problem("", "launch_inventory_unavailable", "The request inventory could not be read completely; retry inspection.")
			return
		}
		next := aws.ToString(out.NextToken)
		if next == "" {
			return
		}
		if tokens[next] {
			r.problem("", "launch_inventory_invalid", "EC2 repeated an inventory pagination token; retry inspection.")
			return
		}
		tokens[next] = true
		input.NextToken = aws.String(next)
	}
}

func (r *launchReconciler) fleet(ctx context.Context, attempt AttemptReceipt) {
	if !fleetIDRE.MatchString(attempt.FleetID) {
		r.problem("", "fleet_identity_mismatch", "The known Fleet identity is invalid; inspect the original request.")
		return
	}
	input := &ec2.DescribeFleetsInput{FleetIds: []string{attempt.FleetID}}
	tokens, seen := map[string]bool{}, false
	for ctx.Err() == nil {
		out, err := r.api.DescribeFleets(ctx, input)
		if out != nil {
			for _, fleet := range out.Fleets {
				if seen || aws.ToString(fleet.FleetId) != attempt.FleetID || !reconciledFleetPins(r.out.Receipt.Plan, attempt, fleet) {
					r.problem(attempt.FleetID, "fleet_identity_mismatch", "EC2 Fleet settings differ from the original token, scope or pinned launch configuration.")
				}
				seen = true
				ids := map[string]bool{}
				for _, group := range fleet.Instances {
					choice, valid := fleetResponsePool(r.out.Receipt.Plan, string(group.InstanceType), "", "", group.LaunchTemplateAndOverrides)
					for _, id := range group.InstanceIds {
						r.retain(attempt.AttemptID, id, choice, nil)
						if !valid || string(group.Lifecycle) != r.out.Receipt.Plan.Market || group.Platform != "" || ids[id] {
							r.problem(id, "fleet_identity_mismatch", "Fleet reported conflicting worker placement or market evidence.")
						}
						ids[id] = true
					}
				}
			}
		}
		// Instant Fleet records expire. Their absence adds no fulfillment evidence
		// and cannot invalidate the original, already complete shared response.
		if apiCode(err, "InvalidFleetId.NotFound") || apiCode(err, "InvalidFleetID.NotFound") {
			return
		}
		if err != nil || out == nil {
			r.problem(attempt.FleetID, "fleet_observation_unavailable", "The known Fleet could not be inspected completely; retry observation.")
			return
		}
		next := aws.ToString(out.NextToken)
		if next == "" {
			return
		}
		if tokens[next] {
			r.problem(attempt.FleetID, "fleet_inventory_invalid", "EC2 repeated a Fleet pagination token; retry inspection.")
			return
		}
		tokens[next] = true
		input.NextToken = aws.String(next)
	}
}

func reconciledFleetPins(plan LaunchPlan, attempt AttemptReceipt, fleet types.FleetData) bool {
	_, tagsOK := fleetObservedTags(fleet.Tags, plan, attempt)
	if !tagsOK || aws.ToString(fleet.ClientToken) != attempt.ClientToken || fleet.Type != types.FleetTypeInstant || aws.ToBool(fleet.ReplaceUnhealthyInstances) || aws.ToBool(fleet.TerminateInstancesWithExpiration) || len(fleet.LaunchTemplateConfigs) != 1 {
		return false
	}
	capacity := fleet.TargetCapacitySpecification
	if capacity == nil || aws.ToInt32(capacity.TotalTargetCapacity) != int32(attempt.RequestedCount) || string(capacity.DefaultTargetCapacityType) != plan.Market || (capacity.TargetCapacityUnitType != "" && capacity.TargetCapacityUnitType != types.TargetCapacityUnitTypeUnits) {
		return false
	}
	if plan.Market == "spot" {
		if aws.ToInt32(capacity.SpotTargetCapacity) != int32(attempt.RequestedCount) || aws.ToInt32(capacity.OnDemandTargetCapacity) != 0 || fleet.SpotOptions == nil || fleet.SpotOptions.AllocationStrategy != types.SpotAllocationStrategyPriceCapacityOptimized || fleet.SpotOptions.MaintenanceStrategies != nil {
			return false
		}
	} else if aws.ToInt32(capacity.OnDemandTargetCapacity) != int32(attempt.RequestedCount) || aws.ToInt32(capacity.SpotTargetCapacity) != 0 || fleet.OnDemandOptions == nil || fleet.OnDemandOptions.AllocationStrategy != types.FleetOnDemandAllocationStrategyLowestPrice {
		return false
	}
	launch := fleet.LaunchTemplateConfigs[0]
	spec := launch.LaunchTemplateSpecification
	if spec == nil || aws.ToString(spec.LaunchTemplateId) != plan.Image.LaunchTemplateID || aws.ToString(spec.Version) != plan.Image.LaunchTemplateVersion || len(launch.Overrides) != len(plan.Choices) {
		return false
	}
	seen := map[config.LaunchChoice]bool{}
	for _, override := range launch.Overrides {
		choice, valid := fleetResponsePool(plan, "", "", "", &types.LaunchTemplateAndOverridesResponse{Overrides: &override})
		if !valid || seen[choice] || aws.ToString(override.ImageId) != plan.Image.AMIID || len(override.BlockDeviceMappings) != 1 || (override.MaxPrice != nil && aws.ToString(override.MaxPrice) != "") {
			return false
		}
		seen[choice] = true
		mapping := override.BlockDeviceMappings[0]
		ebs := mapping.Ebs
		if aws.ToString(mapping.DeviceName) != plan.Image.RootDeviceName || mapping.NoDevice != nil || mapping.VirtualName != nil || ebs == nil || aws.ToInt32(ebs.VolumeSize) != int32(plan.RootDisk.SizeGB) || ebs.VolumeType != types.VolumeTypeGp3 || !aws.ToBool(ebs.Encrypted) || !aws.ToBool(ebs.DeleteOnTermination) {
			return false
		}
	}
	return true
}

func (r *launchReconciler) verify(ctx context.Context) {
	ids := make([]string, 0, len(r.workers))
	for id := range r.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		known := r.workers[id]
		n, ok := r.attempts[known.AttemptID]
		if !ok {
			continue
		}
		attempt := r.out.Receipt.Attempts[n]
		attempt.InstanceIDs = []string{id}
		inspection := &historicalFleetInventory{FleetInventory: r.api, r: r, attempt: attempt, known: known, historical: r.historical[id]}
		workers, err := VerifyFleetWorkers(ctx, inspection, r.out.Receipt.Plan, attempt, []WorkerOutcome{known})
		if len(workers) != 1 {
			r.problem(id, "worker_inventory_invalid", "Exact worker inspection returned contradictory identities.")
			continue
		}
		worker := workers[0]
		worker.Volumes = mergeFleetVolumes(worker.Volumes, r.workers[id].Volumes)
		var inspectionFailure *Failure
		onlyGone := err == nil || (errors.As(err, &inspectionFailure) && inspectionFailure.Code == "worker_observation_unavailable")
		if inspection.historical && inspection.gone && !inspection.failed && !inspection.live && onlyGone {
			worker.Status, worker.ObservationCode = "historical", ""
			if inspection.terminalState != "" {
				worker.State = inspection.terminalState
			}
		} else if err != nil {
			r.problem(id, "worker_observation_unavailable", "A known worker or root volume could not be verified against the original launch pins.")
		}
		r.workers[id] = worker
	}
}

// Inspect historical IDs separately: EC2's NotFound for one removed instance
// must never mask another live instance in the same request. Terminal instances
// can lose network/profile/root observations; present contradictory values still
// fail closed, and their last observed mappings remain available for cleanup.
type historicalFleetInventory struct {
	FleetInventory
	r                              *launchReconciler
	attempt                        AttemptReceipt
	known                          WorkerOutcome
	historical, gone, live, failed bool
	terminalState                  string
	seen                           map[string]bool
}

func (s *historicalFleetInventory) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, options ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	out, err := s.FleetInventory.DescribeInstances(ctx, in, options...)
	if s.historical && apiCode(err, "InvalidInstanceID.NotFound") && out == nil {
		s.gone = true
		return &ec2.DescribeInstancesOutput{}, nil
	}
	if err != nil || out == nil {
		s.failed = true
	}
	if out == nil {
		return out, err
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	copy := *out
	copy.Reservations = make([]types.Reservation, 0, len(out.Reservations))
	count := 0
	for _, reservation := range out.Reservations {
		kept := reservation
		kept.Instances = []types.Instance{}
		for _, instance := range reservation.Instances {
			count++
			actual := record(instance)
			s.r.retain(tagsOf(instance)["AttemptId"], actual.ID, config.LaunchChoice{}, actual.Volumes)
			if s.seen[actual.ID] {
				s.failed = true
				s.r.problem(actual.ID, "worker_inventory_invalid", "Exact EC2 inspection repeated a worker identity; retry inspection.")
			}
			s.seen[actual.ID] = true
			terminal := actual.State == "terminated" || actual.State == "shutting-down"
			if s.historical && actual.ID == s.known.ID && terminal {
				s.gone, s.terminalState = true, actual.State
				if !historicalInstanceMatches(s.r.out.Receipt.Plan, s.attempt, s.known, aws.ToString(reservation.OwnerId), instance) {
					s.failed = true
					s.r.problem(actual.ID, "launch_identity_mismatch", "A historical worker has present settings that contradict the original launch pins.")
				}
				continue
			}
			s.live = true
			kept.Instances = append(kept.Instances, instance)
		}
		copy.Reservations = append(copy.Reservations, kept)
	}
	if count == 0 {
		s.gone = true
	}
	return &copy, err
}

func historicalInstanceMatches(plan LaunchPlan, attempt AttemptReceipt, known WorkerOutcome, owner string, instance types.Instance) bool {
	tags, ok := fleetObservedTags(instance.Tags, plan, attempt)
	if !ok || owner != plan.Account || tags["aws:ec2launchtemplate:id"] != plan.Image.LaunchTemplateID || tags["aws:ec2launchtemplate:version"] != plan.Image.LaunchTemplateVersion {
		return false
	}
	actual := record(instance)
	if (instance.ImageId != nil && actual.Image != plan.Image.AMIID) || (instance.InstanceType != "" && actual.Type != known.Type) || actual.Market != plan.Market || (instance.SubnetId != nil && aws.ToString(instance.SubnetId) != known.SubnetID) || (instance.Placement != nil && instance.Placement.AvailabilityZone != nil && aws.ToString(instance.Placement.AvailabilityZone) != known.AvailabilityZone) || (instance.IamInstanceProfile != nil && aws.ToString(instance.IamInstanceProfile.Arn) != plan.InstanceProfileARN) || (len(instance.SecurityGroups) > 0 && !exactFleetGroup(instance.SecurityGroups, plan.SecurityGroupID)) {
		return false
	}
	if metadata := instance.MetadataOptions; metadata != nil && (metadata.HttpTokens != types.HttpTokensStateRequired || metadata.HttpEndpoint != types.InstanceMetadataEndpointStateEnabled || aws.ToInt32(metadata.HttpPutResponseHopLimit) != 1 || metadata.InstanceMetadataTags != types.InstanceMetadataTagsStateDisabled) {
		return false
	}
	if (instance.RootDeviceName != nil && aws.ToString(instance.RootDeviceName) != plan.Image.RootDeviceName) || (instance.RootDeviceType != "" && instance.RootDeviceType != types.DeviceTypeEbs) || len(instance.BlockDeviceMappings) > 1 || len(instance.NetworkInterfaces) > 1 {
		return false
	}
	for _, mapping := range instance.BlockDeviceMappings {
		if aws.ToString(mapping.DeviceName) != plan.Image.RootDeviceName || mapping.Ebs == nil || !aws.ToBool(mapping.Ebs.DeleteOnTermination) {
			return false
		}
		for _, old := range known.Volumes {
			if old.Root && old.ID != aws.ToString(mapping.Ebs.VolumeId) {
				return false
			}
		}
	}
	for _, nic := range instance.NetworkInterfaces {
		if (nic.SubnetId != nil && aws.ToString(nic.SubnetId) != known.SubnetID) || (nic.OwnerId != nil && aws.ToString(nic.OwnerId) != plan.Account) || (len(nic.Groups) > 0 && !exactFleetGroup(nic.Groups, plan.SecurityGroupID)) || (nic.Attachment != nil && (aws.ToInt32(nic.Attachment.DeviceIndex) != 0 || !aws.ToBool(nic.Attachment.DeleteOnTermination))) {
			return false
		}
	}
	return true
}
