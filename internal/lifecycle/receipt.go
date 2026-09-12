package lifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

// Parameters contains all effective launch overrides and immutable template
// identity. The numeric template holds network, bootstrap and instance profile.
type Parameters struct {
	Account            string       `json:"account"`
	Region             string       `json:"region"`
	Deployment         string       `json:"deployment"`
	Owner              string       `json:"owner"`
	Profile            string       `json:"profile"`
	Name               string       `json:"name"`
	Market             string       `json:"market"`
	InstanceType       string       `json:"instance_type"`
	DiskGB             int          `json:"disk_gb"`
	Image              config.Image `json:"image"`
	SubnetID           string       `json:"subnet_id"`
	SecurityGroupID    string       `json:"security_group_id"`
	InstanceProfileARN string       `json:"instance_profile_arn"`
	BootstrapSHA256    string       `json:"bootstrap_sha256"`
}
type Receipt struct {
	SchemaVersion   int        `json:"schema_version"`
	RequestID       string     `json:"request_id"`
	ClientToken     string     `json:"client_token"`
	CreatedAt       string     `json:"created_at"`
	State           string     `json:"state"` // prepared -> dispatched -> observed; never moves back
	Parameters      Parameters `json:"parameters"`
	InstanceIDs     []string   `json:"instance_ids"`
	LaunchErrorCode string     `json:"launch_error_code,omitempty"`
}

var requestRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidRequest(s string) bool { return requestRE.MatchString(s) }
func newReceipt(p Parameters) (Receipt, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Receipt{}, failure("receipt_unavailable", "cannot generate a launch request identity; no instance launched")
	}
	id := hex.EncodeToString(b)
	return Receipt{SchemaVersion: 1, RequestID: id, ClientToken: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "prepared", Parameters: p, InstanceIDs: []string{}}, nil
}
func parameters(c config.Config, m config.Manifest, p config.Profile, name string) Parameters {
	return Parameters{Account: c.ExpectedAccount, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner, Profile: p.Name, Name: name, Market: "on-demand", InstanceType: p.InstanceTypes[0], DiskGB: p.DiskGB, Image: m.Images[p.Image], SubnetID: m.SubnetIDs[0], SecurityGroupID: m.SecurityGroupID, InstanceProfileARN: m.InstanceProfileARN, BootstrapSHA256: m.BootstrapSHA256}
}
func (r Receipt) validate(id string) error {
	p := r.Parameters
	_, dateErr := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if r.SchemaVersion != 1 || !ValidRequest(id) || r.RequestID != id || r.ClientToken != id || dateErr != nil || !ValidName(p.Name) || p.Profile != "agent" || p.Market != "on-demand" || p.Account == "" || p.Region == "" || p.Deployment == "" || p.Owner == "" || p.Image.LaunchTemplateID == "" || p.Image.LaunchTemplateVersion == "" || p.Image.AMIID == "" || p.InstanceType == "" || p.DiskGB < 8 || p.DiskGB > 16384 {
		return failure("receipt_invalid", "request receipt schema or launch parameters are invalid; restore an intact receipt; use ls and down for AWS inventory and cleanup")
	}
	if r.State != "prepared" && r.State != "dispatched" && r.State != "observed" {
		return failure("receipt_invalid", "request receipt has an invalid state; restore an intact receipt")
	}
	if (r.State == "prepared" && len(r.InstanceIDs) != 0) || (r.State == "observed" && len(r.InstanceIDs) == 0) {
		return failure("receipt_invalid", "request receipt state and observed identities disagree; restore an intact receipt")
	}
	if r.LaunchErrorCode != "" && (r.State != "dispatched" || launchFailure(r.LaunchErrorCode) == nil) {
		return failure("receipt_invalid", "request receipt has an invalid recorded launch error; restore an intact receipt")
	}
	for _, id := range r.InstanceIDs {
		if !instanceRE.MatchString(id) {
			return failure("receipt_invalid", "request receipt contains an invalid instance identity")
		}
	}
	return nil
}

type Store struct{ Dir string }

func DefaultStore() (Store, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, failure("receipt_unavailable", "cannot locate state directory; set XDG_STATE_HOME to an absolute directory")
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return Store{}, failure("receipt_unavailable", "XDG_STATE_HOME must be an absolute directory")
	}
	return Store{Dir: filepath.Join(base, "devbox", "requests")}, nil
}
func (s Store) Path(id string) string { return filepath.Join(s.Dir, id+".json") }
func (s Store) Load(id string) (Receipt, error) {
	var r Receipt
	if !ValidRequest(id) {
		return r, failure("request_invalid", "--resume requires the 32-character request ID printed by up")
	}
	b, err := os.ReadFile(s.Path(id))
	if err != nil {
		return r, failure("receipt_unavailable", "cannot read request receipt; restore it in the state directory; ls and down remain available without receipts")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, failure("receipt_invalid", "invalid request receipt JSON; restore an intact receipt")
	}
	return r, r.validate(id)
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func durableDir(path string) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return os.ErrInvalid
		}
		return nil
	}
	parent := filepath.Dir(path)
	if parent == path {
		return os.ErrInvalid
	}
	if err := durableDir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return syncDir(parent)
}
func (s Store) Save(r Receipt) error {
	fail := failure("receipt_unavailable", "cannot durably save request receipt; inspect the reported request/instance IDs before any further allocation")
	if r.validate(r.RequestID) != nil {
		return fail
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fail
	}
	f, err := os.CreateTemp(s.Dir, ".receipt-")
	if err != nil {
		return fail
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return fail
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fail
	}
	if err = f.Close(); err != nil {
		return fail
	}
	if err = os.Rename(f.Name(), s.Path(r.RequestID)); err != nil {
		return fail
	}
	if err = syncDir(s.Dir); err != nil {
		return fail
	}
	return nil
}
func (s Store) Lock(ctx context.Context, id string) (func(), error) {
	if !ValidRequest(id) {
		return nil, failure("request_invalid", "invalid request identity")
	}
	if err := durableDir(s.Dir); err != nil {
		return nil, failure("receipt_unavailable", "cannot create durable request directory; check state directory permissions")
	}
	return lockReceipt(ctx, filepath.Join(s.Dir, id+".lock"))
}
