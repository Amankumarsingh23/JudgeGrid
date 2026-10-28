package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ResourceNamespace = "judgegrid-local-v2"

var resourceID = regexp.MustCompile(`^judgegrid-sandbox-[a-z2-7]{26}$`)
var uuidID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var controllerID = regexp.MustCompile(`^[a-z2-7]{26}$`)
var hexHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var roleID = regexp.MustCompile(`^(artifact|compile|prepare|run|run-([2-9]|1[0-6]))$`)

type Provenance struct {
	ExecutionID, ControllerID, RunID, SubmissionID, OwnerHash, Created string
	Generation                                                         int
}

func (p Provenance) Validate() error {
	t, e := time.Parse(time.RFC3339Nano, p.Created)
	if !resourceID.MatchString(p.ExecutionID) || !controllerID.MatchString(p.ControllerID) || !uuidID.MatchString(p.RunID) || !uuidID.MatchString(p.SubmissionID) || !hexHash.MatchString(p.OwnerHash) || p.Generation < 1 || p.Generation > 5 || e != nil || t.Location() != time.UTC {
		return ErrInvalid
	}
	return nil
}
func OwnerHash(owner string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(owner))) }
func (p Provenance) Labels(role string) map[string]string {
	return map[string]string{"judgegrid.sandbox": "true", "judgegrid.namespace": ResourceNamespace, "judgegrid.execution": p.ExecutionID, "judgegrid.controller": p.ControllerID, "judgegrid.run": p.RunID, "judgegrid.submission": p.SubmissionID, "judgegrid.generation": strconv.Itoa(p.Generation), "judgegrid.owner": p.OwnerHash, "judgegrid.created": p.Created, "judgegrid.role": role}
}
func ParseLabels(m map[string]string) (Provenance, string, error) {
	g, e := strconv.Atoi(m["judgegrid.generation"])
	p := Provenance{m["judgegrid.execution"], m["judgegrid.controller"], m["judgegrid.run"], m["judgegrid.submission"], m["judgegrid.owner"], m["judgegrid.created"], g}
	role := m["judgegrid.role"]
	if e != nil || m["judgegrid.namespace"] != ResourceNamespace || m["judgegrid.sandbox"] != "true" || !roleID.MatchString(role) || p.Validate() != nil {
		return p, role, ErrInvalid
	}
	if !labelsEqual(m, p.Labels(role)) {
		return p, role, ErrInvalid
	}
	for k, v := range p.Labels(role) {
		if m[k] != v {
			return p, role, ErrInvalid
		}
	}
	return p, role, nil
}

type Registry interface {
	RegisterSandbox(context.Context, Provenance) error
	ProtectSandbox(context.Context, string, func() error) error
	FinishSandbox(context.Context, string) error
}
type binding struct {
	run, sub, owner string
	generation      int
}
type identityKey struct{}
type registryKey struct{}

func WithIdentity(ctx context.Context, run, sub, owner string, generation int) context.Context {
	return context.WithValue(ctx, identityKey{}, binding{run, sub, owner, generation})
}
func WithRegistry(ctx context.Context, r Registry) context.Context {
	return context.WithValue(ctx, registryKey{}, r)
}
func randomUUID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func labelArgs(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := []string{}
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}
func ResourceName(p Provenance, role string) string {
	if role == "artifact" {
		return p.ExecutionID + "-work"
	}
	return p.ExecutionID + "-" + role
}
func labelsEqual(a, b map[string]string) bool {
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	for k := range a {
		if strings.HasPrefix(k, "judgegrid.") {
			if _, ok := b[k]; !ok {
				return false
			}
		}
	}
	return true
}
