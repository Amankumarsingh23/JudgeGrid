package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Resource struct {
	Kind, ID, Name, Created string
	Labels                  map[string]string
	Running                 bool
}

func InspectResource(ctx context.Context, kind, id string) (Resource, error) {
	r := Resource{Kind: kind}
	var args []string
	if kind == "container" {
		args = []string{"inspect", "--type", "container", "--format", "{{json .}}", id}
	} else if kind == "volume" {
		args = []string{"volume", "inspect", "--format", "{{json .}}", id}
	} else {
		return r, ErrInvalid
	}
	data, e := control(ctx, args...)
	if e != nil {
		return r, e
	}
	var v struct {
		ID, Name, Created, CreatedAt string
		Labels                       map[string]string
		Config                       struct{ Labels map[string]string }
		State                        struct{ Running bool }
	}
	if e = json.Unmarshal(data, &v); e != nil {
		return r, e
	}
	r.ID = v.ID
	r.Name = strings.TrimPrefix(v.Name, "/")
	r.Created = v.Created
	r.Labels = v.Config.Labels
	r.Running = v.State.Running
	if kind == "volume" {
		r.ID = v.Name
		r.Created = v.CreatedAt
		r.Labels = v.Labels
	}
	return r, nil
}
func OwnedResource(r Resource, p Provenance, role string) bool {
	parsed, actual, e := ParseLabels(r.Labels)
	return e == nil && parsed == p && actual == role && r.Name == ResourceName(p, role) && ((r.Kind == "volume" && role == "artifact") || (r.Kind == "container" && role != "artifact"))
}
func RemoveOwned(ctx context.Context, kind, name string, p Provenance, role string) error {
	r, e := InspectResource(ctx, kind, name)
	if e != nil {
		return e
	}
	if !OwnedResource(r, p, role) {
		return fmt.Errorf("refused mismatched resource identity")
	}
	return DeleteResource(ctx, r)
}
func DeleteResource(ctx context.Context, expected Resource) error {
	live, e := InspectResource(ctx, expected.Kind, expected.ID)
	if e != nil {
		if missingObject(e) {
			return nil
		}
		return e
	}
	p, role, e := ParseLabels(expected.Labels)
	if e != nil || !OwnedResource(live, p, role) || live.ID != expected.ID || live.Created != expected.Created {
		return fmt.Errorf("resource changed since inspection")
	}
	if expected.Kind == "container" {
		_, e = control(ctx, "rm", "--force", expected.ID)
	} else {
		_, e = control(ctx, "volume", "rm", expected.ID)
	}
	if e != nil && missingObject(e) {
		return nil
	}
	return e
}
func ListResources(ctx context.Context, max int) ([]Resource, error) {
	if max < 1 || max > 128 {
		return nil, ErrInvalid
	}
	result := []Resource{}
	for _, kind := range []string{"container", "volume"} {
		args := []string{"ps", "-aq", "--no-trunc", "--filter", "label=judgegrid.namespace=" + ResourceNamespace}
		if kind == "volume" {
			args = []string{"volume", "ls", "-q", "--filter", "label=judgegrid.namespace=" + ResourceNamespace}
		}
		data, e := control(ctx, args...)
		if e != nil {
			return nil, e
		}
		ids := strings.Fields(string(data))
		if len(result)+len(ids) > max {
			return nil, fmt.Errorf("resource inventory exceeds explicit scan limit")
		}
		for _, id := range ids {
			r, e := InspectResource(ctx, kind, id)
			if e != nil {
				if missingObject(e) {
					continue
				}
				return nil, e
			}
			result = append(result, r)
		}
	}
	return result, nil
}
