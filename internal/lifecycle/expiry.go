package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type fixedClock struct{ time.Time }

func (c fixedClock) Now() time.Time { return c.Time }
func clockNow(clock expiry.Clock) time.Time {
	if clock == nil {
		clock = expiry.SystemClock{}
	}
	return clock.Now()
}
func expiryFailure(err error) error {
	var e *expiry.Error
	if !errors.As(err, &e) {
		return err
	}
	messages := map[expiry.Reason]string{
		expiry.TTLInvalid:     "TTL must be an unsigned positive Go duration no greater than 168h; default_ttl is validated even when --ttl overrides it.",
		expiry.ClockInvalid:   "The clock cannot establish a valid UTC lifetime; no allocation performed.",
		expiry.ExpiryInvalid:  "The immutable expiry window or creation tags are invalid; inspect the original request.",
		expiry.RequestExpired: "The original request has expired and cannot allocate more workers; create a new request. Observation and down remain available.",
		expiry.LegacyRequest:  "Legacy request has no expiry and cannot allocate more workers; create a new request. Observation and down remain available.",
	}
	return failure(string(e.Code), messages[e.Code])
}
func requireExpiryManifest(m config.Manifest) error {
	if m.SchemaVersion != expiry.ManifestSchema {
		return failure("manifest_upgrade_required", "New allocation requires the expiry-capable manifest v6 and its tag authorization from foundation #46; v4/v5 support observation and down only.")
	}
	return nil
}
func supportedPlan(schema int) bool { return schema == 1 || schema == expiry.LaunchPlanSchema }
func batchVersion(planSchema int) int {
	if planSchema == 1 {
		return 2
	}
	if planSchema == expiry.LaunchPlanSchema {
		return expiry.BatchReceiptSchema
	}
	return 0
}
func validBatchVersion(receipt, plan int) bool {
	return supportedPlan(plan) && receipt == batchVersion(plan)
}
func (p LaunchPlan) Window() expiry.Window {
	return expiry.Window{CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt}
}
func validatePlanExpiry(p LaunchPlan) error {
	return expiry.ValidatePlanFields(p.SchemaVersion, p.Window(), p.CreationTags)
}
func checkAllocation(p LaunchPlan, clock expiry.Clock) error {
	return expiryFailure(expiry.CheckAllocation(p.SchemaVersion, p.Window(), clockNow(clock), false))
}
func (p LaunchPlan) ExpiryPreview() string {
	if p.SchemaVersion == 1 || p.SchemaVersion == 0 {
		return "ttl=null expires_at=null"
	}
	return p.Window().String()
}

// UnmarshalJSON retains strict unknown-field checking inside every enclosing
// receipt/ledger decoder and rejects present empty/null legacy expiry fields.
// It never normalizes historical strings or changes their canonical encoding.
func (p *LaunchPlan) UnmarshalJSON(data []byte) error {
	type plain LaunchPlan
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var value plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid launch plan JSON")
	}
	if value.SchemaVersion == 1 {
		// encoding/json matches field names with Unicode case folding too.
		// Reject presence even when an empty/null value would disappear through
		// omitempty and leave the historical digest apparently unchanged.
		for key := range fields {
			if strings.EqualFold(key, "expires_at") {
				return errors.New("legacy launch plan cannot contain expires_at")
			}
		}
	}
	// Zero plans are used in incomplete public outcomes, but never validate as
	// immutable ledger plans. Strict validators reject them at that boundary.
	if value.SchemaVersion != 0 {
		if err := validatePlanExpiry(LaunchPlan(value)); err != nil {
			return err
		}
	}
	*p = LaunchPlan(value)
	return nil
}

func inspectInstanceExpiry(i *Instance, tags []types.Tag, now time.Time) {
	raw := make([]expiry.Tag, 0, len(tags))
	for _, tag := range tags {
		raw = append(raw, expiry.Tag{Key: tag.Key, Value: tag.Value})
	}
	value, reason := expiry.InspectTags(raw)
	i.ExpiresAt = ""
	switch reason {
	case expiry.ExpiryMissing:
		i.ExpiryStatus = "missing"
	case expiry.ExpiryDuplicate:
		i.ExpiryStatus = "duplicate"
	case expiry.ExpiryInvalid:
		i.ExpiryStatus = "invalid"
	default:
		i.ExpiresAt = *value
		setExpiryStatus(i, now)
	}
}
func setExpiryStatus(i *Instance, now time.Time) {
	if i.ExpiresAt == "" {
		if i.ExpiryStatus == "" {
			i.ExpiryStatus = "missing"
		}
		return
	}
	deadline, err := expiry.ParseTimestamp(i.ExpiresAt)
	if err != nil {
		i.ExpiresAt, i.ExpiryStatus = "", "invalid"
		return
	}
	i.ExpiryStatus = "expired"
	if now.Before(deadline) {
		i.ExpiryStatus = "future"
	}
}

// PublicInstance adds required nullable expiry without changing the permanent
// response-v1 worker wire shape. Historic zero-value fields remain omitted there.
type PublicInstance struct {
	Instance
	ExpiresAt    *string `json:"expires_at"`
	ExpiryStatus string  `json:"expiry_status"`
}

func ProjectInstance(i Instance) PublicInstance {
	status := i.ExpiryStatus
	if status == "" {
		status = "missing"
	}
	var expires *string
	if i.ExpiresAt != "" {
		v := i.ExpiresAt
		expires = &v
	}
	return PublicInstance{Instance: i, ExpiresAt: expires, ExpiryStatus: status}
}
func PublicResult(r Result) any {
	type plain Result
	instances := make([]PublicInstance, 0, len(r.Instances))
	for _, i := range r.Instances {
		instances = append(instances, ProjectInstance(i))
	}
	return struct {
		plain
		Instances []PublicInstance `json:"instances"`
	}{plain(r), instances}
}
func (r BatchResult) MarshalJSON() ([]byte, error) {
	type plain BatchResult
	type worker struct {
		PublicInstance
		Status string `json:"status"`
	}
	workers := make([]worker, 0, len(r.Workers))
	for _, w := range r.Workers {
		workers = append(workers, worker{ProjectInstance(w.Instance), w.Status})
	}
	setBatchExpiry(&r.BatchOutcome)
	if r.Attempts == nil {
		r.Attempts = []AttemptOutcome{}
	}
	if r.Errors == nil {
		r.Errors = []ResourceError{}
	}
	return json.Marshal(struct {
		plain
		Workers []worker `json:"instances"`
	}{plain(r), workers})
}

func setBatchExpiry(out *BatchOutcome) {
	out.TTL, out.ExpiresAt = nil, nil
	if duration, err := out.Plan.Window().TTL(); err == nil && out.Plan.SchemaVersion == expiry.LaunchPlanSchema {
		value := duration.String()
		out.TTL = &value
		deadline := out.Plan.ExpiresAt
		out.ExpiresAt = &deadline
	}
}
