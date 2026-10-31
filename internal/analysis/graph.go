package analysis

import (
	"context"
	"path"
	"sort"
	"strings"
)

// Resolution is intentionally syntactic. No global short-name index is consulted.
func relationships(ctx context.Context, m *Manifest, units []unit, p Policy) error {
	budget := false
	check := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if budget || len(m.Files)+len(m.Symbols)+len(m.Imports)+len(m.Calls)+len(m.Edges)+len(m.Chunks) > p.MaxRows {
			return ErrBudget
		}
		return nil
	}
	byPath := map[string]*unit{}
	byID := map[string]*unit{}
	module := ""
	for i := range units {
		u := &units[i]
		byPath[u.file.Path] = u
		byID[u.file.ID] = u
		if u.file.Path == "go.mod" {
			for _, line := range strings.Split(string(u.bytes), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "module" {
					module = strings.Trim(fields[1], "\"")
					break
				}
			}
		}
	}
	edge := func(f, from, kind, class, tk, target string, s Span) {
		if check() != nil {
			budget = true
			return
		}
		v := Edge{File: f, From: from, Kind: kind, Class: class, TargetKind: tk, Target: target, Span: s}
		v.ID = hash([]any{"edge-v1", m.Index, v})
		m.Edges = append(m.Edges, v)
	}
	candidates := func(u *unit, name, receiver string, top bool) []string {
		out := []string{}
		for _, s := range u.symbols {
			if s.Name == name && s.Receiver == receiver && (!top || s.Parent == "") {
				out = append(out, s.ID)
			}
		}
		return out
	}
	unique := func(a []string) []string {
		sort.Strings(a)
		out := []string{}
		for _, s := range a {
			if len(out) == 0 || s != out[len(out)-1] {
				out = append(out, s)
			}
		}
		return out
	}
	for i := range units {
		if e := check(); e != nil {
			return e
		}
		u := &units[i]
		if u.file.State == "EXCLUDED" {
			continue
		}
		for _, s := range u.symbols {
			from := u.file.ID
			if s.Parent != "" {
				from = s.Parent
			}
			edge(u.file.ID, from, "CONTAINS", "resolved", "symbol", s.ID, s.NameSpan)
			if s.Receiver != "" {
				targets := []string{}
				for j := range units {
					v := &units[j]
					if v.file.Language == "go" && path.Dir(v.file.Path) == path.Dir(u.file.Path) && v.file.Package == u.file.Package {
						for _, t := range v.symbols {
							if t.Parent == "" && t.Name == s.Receiver && (t.Kind == "struct" || t.Kind == "interface" || t.Kind == "type") {
								targets = append(targets, t.ID)
							}
						}
					}
				}
				class := "candidate"
				if len(targets) == 1 {
					class = "resolved"
				}
				for _, t := range unique(targets) {
					edge(u.file.ID, s.ID, "RECEIVER_OF", class, "symbol", t, s.NameSpan)
				}
			} else if s.Kind == "method" && s.Parent != "" {
				edge(u.file.ID, s.ID, "RECEIVER_OF", "resolved", "symbol", s.Parent, s.NameSpan)
			}
		}
		imports := []Import{}
		for _, raw := range u.facts.Imports {
			if e := check(); e != nil {
				return e
			}
			v := Import{File: u.file.ID, Module: raw.Module, Name: raw.Name, Alias: raw.Alias, Class: "external", Span: span(u.bytes, raw.Start, raw.End), Targets: []string{}}
			paths := []string{}
			switch u.file.Language {
			case "go":
				if module != "" && (raw.Module == module || strings.HasPrefix(raw.Module, module+"/")) {
					dir := strings.TrimPrefix(strings.TrimPrefix(raw.Module, module), "/")
					if dir == "" {
						dir = "."
					}
					for j := range units {
						t := &units[j]
						if t.file.Language == "go" && path.Dir(t.file.Path) == dir && t.file.Grammar != "" {
							paths = append(paths, t.file.Path)
						}
					}
					v.Class = "unresolved"
				}
			case "python":
				if strings.HasPrefix(raw.Module, ".") {
					dots := len(raw.Module) - len(strings.TrimLeft(raw.Module, "."))
					base := path.Dir(u.file.Path)
					for n := 1; n < dots; n++ {
						base = path.Dir(base)
					}
					tail := strings.ReplaceAll(raw.Module[dots:], ".", "/")
					target := path.Join(base, tail)
					paths = []string{target + ".py", path.Join(target, "__init__.py")}
					v.Class = "unresolved"
				}
			case "javascript", "typescript":
				if strings.HasPrefix(raw.Module, ".") {
					base := path.Join(path.Dir(u.file.Path), raw.Module)
					paths = []string{base}
					for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", "/index.js", "/index.ts"} {
						paths = append(paths, base+ext)
					}
					v.Class = "unresolved"
				}
			}
			for _, p := range paths {
				if t := byPath[p]; t != nil && t.file.State != "EXCLUDED" && t.file.Grammar != "" {
					v.Targets = append(v.Targets, t.file.ID)
				}
			}
			v.Targets = unique(v.Targets)
			if len(v.Targets) > 0 {
				v.Class = "candidate"
				if u.file.Language == "go" || len(v.Targets) == 1 {
					v.Class = "resolved"
				}
			}
			if raw.Alias == "." || raw.Alias == "_" {
				v.Class = "unresolved"
			}
			v.ID = hash([]any{"import-v1", m.Index, v})
			imports = append(imports, v)
			m.Imports = append(m.Imports, v)
			if len(v.Targets) == 0 {
				edge(u.file.ID, u.file.ID, "IMPORTS", v.Class, "module", v.Module, v.Span)
			} else {
				for _, target := range v.Targets {
					edge(u.file.ID, u.file.ID, "IMPORTS", v.Class, "file", target, v.Span)
				}
			}
		}
		for _, raw := range u.facts.Calls {
			if e := check(); e != nil {
				return e
			}
			v := Call{File: u.file.ID, Target: raw.Target, Class: "unresolved", Reason: "dynamic-or-unknown", Span: span(u.bytes, raw.Start, raw.End), Targets: []string{}}
			if raw.Parent >= 0 {
				v.Parent = u.symbols[raw.Parent].ID
			}
			target := strings.TrimSpace(raw.Target)
			parts := strings.Split(target, ".")
			root := parts[0]
			blocked := false
			var binding *Binding
			for j := range u.facts.Bindings {
				b := &u.facts.Bindings[j]
				if b.Name == root && raw.Start >= b.ScopeStart && raw.End <= b.ScopeEnd {
					isDecl := false
					for _, s := range u.symbols {
						if s.Name == root && s.NameSpan.Start == b.Start {
							isDecl = true
						}
					}
					if !isDecl {
						blocked = true
						if binding == nil || b.ScopeEnd-b.ScopeStart < binding.ScopeEnd-binding.ScopeStart {
							binding = b
						}
					}
				}
			}
			if len(parts) <= 2 && !strings.ContainsAny(target, "[]() \n\t?/") {
				if !blocked {
					for _, im := range imports {
						if im.Alias != root || im.Alias == "." || im.Alias == "_" {
							continue
						}
						name := ""
						if len(parts) == 2 {
							name = parts[1]
						} else if u.file.Language != "go" {
							name = im.Name
						}
						if im.Class == "external" {
							v.Class = "external"
							v.Reason = "explicit-external-import"
						} else if name != "" {
							if im.Class == "candidate" {
								v.Class = "candidate"
							}
							for _, id := range im.Targets {
								t := byID[id]
								for _, s := range t.symbols {
									if s.Name == name && s.Parent == "" && s.Receiver == "" && (u.file.Language != "go" || s.Export == "public") && ((u.file.Language != "javascript" && u.file.Language != "typescript") || s.Export == "exported") {
										v.Targets = append(v.Targets, s.ID)
									}
								}
							}
							v.Reason = "explicit-local-import"
						}
					}
				}
				if len(v.Targets) == 0 && v.Class != "external" && len(parts) == 1 && !blocked {
					// Walk lexical owners first; Python class bodies are not closure scopes for methods.
					owner := v.Parent
					for {
						local := []string{}
						for _, s := range u.symbols {
							if s.Name == target && s.Parent == owner && s.Receiver == "" && raw.Start >= s.Scope.Start && raw.End <= s.Scope.End {
								local = append(local, s.ID)
							}
						}
						if owner == "" && u.file.Language == "go" {
							for j := range units {
								t := &units[j]
								if t.file.ID != u.file.ID && t.file.Language == "go" && path.Dir(t.file.Path) == path.Dir(u.file.Path) && t.file.Package == u.file.Package {
									local = append(local, candidates(t, target, "", true)...)
								}
							}
						}
						if len(local) > 0 {
							v.Targets = local
							break
						}
						if owner == "" {
							break
						}
						next := ""
						for _, s := range u.symbols {
							if s.ID == owner {
								next = s.Parent
								break
							}
						}
						if u.file.Language != "go" && next != "" {
							for _, s := range u.symbols {
								if s.ID == next && s.Kind == "class" {
									next = s.Parent
								}
							}
						}
						owner = next
					}
					if len(v.Targets) == 0 && u.file.Language == "go" {
						for j := range units {
							t := &units[j]
							if t.file.Language == "go" && path.Dir(t.file.Path) == path.Dir(u.file.Path) && t.file.Package == u.file.Package {
								v.Targets = append(v.Targets, candidates(t, target, "", true)...)
							}
						}
					}
					v.Reason = "lexical-or-package-declaration"
				}
				if len(v.Targets) == 0 && len(parts) == 2 && blocked && binding != nil && u.file.Language == "go" && binding.Type != "" && !strings.ContainsAny(binding.Type, ".[]{} ") {
					concrete := false
					for j := range units {
						t := &units[j]
						if t.file.Language == "go" && path.Dir(t.file.Path) == path.Dir(u.file.Path) && t.file.Package == u.file.Package {
							for _, s := range t.symbols {
								if s.Name == binding.Type && s.Kind == "struct" {
									concrete = true
								}
							}
							v.Targets = append(v.Targets, candidates(t, parts[1], binding.Type, true)...)
						}
					}
					v.Reason = "receiver-type-syntax"
					if !concrete {
						v.Class = "candidate"
					}
				}
				if len(v.Targets) == 0 && len(parts) == 2 && u.file.Language == "python" && (root == "self" || root == "cls") && raw.Parent >= 0 {
					owner := u.symbols[raw.Parent].Parent
					for _, s := range u.symbols {
						if owner != "" && s.Parent == owner && s.Name == parts[1] {
							v.Targets = append(v.Targets, s.ID)
						}
					}
					v.Class = "candidate"
					v.Reason = "dynamic-class-dispatch"
				}
			}
			v.Targets = unique(v.Targets)
			for _, targetID := range v.Targets {
				for j := range units {
					for _, targetSymbol := range units[j].symbols {
						if targetSymbol.ID == targetID && targetSymbol.State == "PARTIAL" {
							v.Class = "candidate"
						}
						if targetSymbol.ID == targetID && targetSymbol.Kind != "function" && targetSymbol.Kind != "method" {
							v.Class = "candidate"
							v.Reason = "callable-value-or-conversion"
						}
					}
				}
			}
			if len(v.Targets) > 0 && v.Class != "candidate" {
				v.Class = "resolved"
				if len(v.Targets) > 1 {
					v.Class = "candidate"
				}
			}
			if raw.Partial || u.file.State == "PARTIAL" {
				if v.Class == "resolved" {
					v.Class = "candidate"
				}
				v.Reason = "partial-syntax"
			}
			if blocked && len(v.Targets) == 0 {
				v.Reason = "shadowed-or-dynamic"
			}
			v.ID = hash([]any{"call-v1", m.Index, v})
			m.Calls = append(m.Calls, v)
			from := v.Parent
			if from == "" {
				from = u.file.ID
			}
			if len(v.Targets) == 0 {
				edge(u.file.ID, from, "CALLS", v.Class, "syntax", v.Target, v.Span)
			} else {
				for _, t := range v.Targets {
					edge(u.file.ID, from, "CALLS", v.Class, "symbol", t, v.Span)
				}
			}
		}
		if u.file.Language == "go" {
			for _, r := range u.facts.References {
				if e := check(); e != nil {
					return e
				}
				targets := []string{}
				for j := range units {
					t := &units[j]
					if t.file.Language == "go" && path.Dir(t.file.Path) == path.Dir(u.file.Path) && t.file.Package == u.file.Package {
						for _, s := range t.symbols {
							if s.Name == r.Name && s.Parent == "" && (s.Kind == "type" || s.Kind == "struct" || s.Kind == "interface") {
								targets = append(targets, s.ID)
							}
						}
					}
				}
				class := "candidate"
				if len(targets) != 1 || u.file.State == "PARTIAL" {
					class = "candidate"
				}
				from := u.file.ID
				if r.Parent >= 0 {
					from = u.symbols[r.Parent].ID
				}
				for _, t := range unique(targets) {
					edge(u.file.ID, from, "REFERENCES", class, "symbol", t, span(u.bytes, r.Start, r.End))
				}
			}
		}
	}
	// Ordering and deduplication make output independent of hash-map iteration.
	sort.Slice(m.Edges, func(i, j int) bool { return m.Edges[i].ID < m.Edges[j].ID })
	out := []Edge{}
	for _, e := range m.Edges {
		if len(out) == 0 || out[len(out)-1].ID != e.ID {
			out = append(out, e)
		}
	}
	m.Edges = out
	return check()
}
