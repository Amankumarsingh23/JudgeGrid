// Package reconcile performs bounded, provenance-checked orphan cleanup on a trusted host.
package reconcile

import (
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"sort"
)

type Authority interface {
	ReconcileSandbox(context.Context, execution.Provenance, bool, func() error) (string, error)
}
type Runtime interface {
	List(context.Context, int) ([]execution.Resource, error)
	Delete(context.Context, execution.Resource) error
}
type Docker struct{}

func (Docker) List(ctx context.Context, max int) ([]execution.Resource, error) {
	return execution.ListResources(ctx, max)
}
func (Docker) Delete(ctx context.Context, r execution.Resource) error {
	return execution.DeleteResource(ctx, r)
}

type Entry struct {
	ExecutionID string `json:"execution_id"`
	Decision    string `json:"decision"`
	Resources   int    `json:"resources"`
}

func Run(ctx context.Context, db Authority, runtime Runtime, apply bool, max int) ([]Entry, error) {
	resources, e := runtime.List(ctx, max)
	if e != nil {
		return nil, e
	}
	groups := map[string][]execution.Resource{}
	identities := map[string]execution.Provenance{}
	invalid := map[string]bool{}
	for _, r := range resources {
		p, role, e := execution.ParseLabels(r.Labels)
		key := r.Labels["judgegrid.execution"]
		if key == "" {
			key = "invalid"
		}
		groups[key] = append(groups[key], r)
		if e != nil || !execution.OwnedResource(r, p, role) {
			invalid[key] = true
			continue
		}
		if old, ok := identities[key]; ok && old != p {
			invalid[key] = true
		}
		identities[key] = p
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	report := []Entry{}
	for _, k := range keys {
		row := Entry{ExecutionID: k, Resources: len(groups[k])}
		if invalid[k] {
			row.ExecutionID = "invalid-provenance"
			row.Decision = "mismatch"
			report = append(report, row)
			continue
		}
		p := identities[k]
		decision, err := db.ReconcileSandbox(ctx, p, apply, func() error {
			// Re-enumerate under the database execution lock: catch late creation and changed labels.
			current, e := runtime.List(ctx, max)
			if e != nil {
				return e
			}
			selected := []execution.Resource{}
			for _, r := range current {
				if r.Labels["judgegrid.execution"] == k {
					actual, role, e := execution.ParseLabels(r.Labels)
					if e != nil || actual != p || !execution.OwnedResource(r, p, role) {
						return fmt.Errorf("resource relationship changed")
					}
					selected = append(selected, r)
				}
			}
			sort.Slice(selected, func(i, j int) bool { return selected[i].Kind < selected[j].Kind })
			for _, r := range selected {
				if e = runtime.Delete(ctx, r); e != nil {
					return e
				}
			}
			return nil
		})
		row.Decision = decision
		report = append(report, row)
		if err != nil {
			return report, err
		}
	}
	return report, nil
}
