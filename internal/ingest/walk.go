package ingest

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path"
)

// Unlike fs.WalkDir, this never reads an entire attacker-sized directory into
// memory just to sort it before enforcing the entry budget. Git inventory order
// comes from pinned objects; audit results are sorted only after bounded collection.
func boundedWalk(ctx context.Context, root *os.Root, start string, maxDepth, maxVisits int, visit fs.WalkDirFunc) error {
	count := 0
	var walk func(string, int) error
	walk = func(name string, depth int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		if depth > maxDepth || count > maxVisits {
			return ErrBudget
		}
		info, e := root.Lstat(name)
		if e != nil {
			return visit(name, nil, e)
		}
		entry := fs.FileInfoToDirEntry(info)
		if e = visit(name, entry, nil); e != nil {
			return e
		}
		if !entry.IsDir() {
			return nil
		}
		dir, e := root.Open(name)
		if e != nil {
			return e
		}
		defer dir.Close()
		for {
			children, e := dir.ReadDir(64)
			for _, child := range children {
				if x := walk(path.Join(name, child.Name()), depth+1); x != nil {
					return x
				}
			}
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
		}
	}
	return walk(start, 1)
}
