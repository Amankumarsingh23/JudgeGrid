package workspace

import "context"

type VerifiedFile struct {
	File
	Bytes []byte
}

// ExportSelected pins one authorized inventory, omits its reserved/directory
// entries, and verifies every emitted regular file before returning any bytes.
func (m WorkspaceManager) ExportSelected(ctx context.Context, owner, id, expected string) (Workspace, []VerifiedFile, error) {
	w, err := m.scope(ctx, owner, id)
	if err != nil {
		return w, nil, err
	}
	if w.Selected != expected {
		return w, nil, ErrConflict
	}
	w.Files, err = m.inventory(ctx, w, expected)
	if err != nil {
		return w, nil, err
	}
	files := make([]VerifiedFile, 0, len(w.Files))
	for _, f := range w.Files {
		if f.Hash == "" {
			continue
		}
		b, e := m.object(ctx, id, f)
		if e != nil {
			return w, nil, e
		}
		files = append(files, VerifiedFile{File: f, Bytes: b})
	}
	return w, files, nil
}
