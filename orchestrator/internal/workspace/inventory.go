package workspace

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"sort"
	"strings"
	"time"
)

type Entry struct {
	Name, Type string
	Size       int64
	ModifiedAt time.Time
}
type DirectoryPage struct {
	Entries []Entry
	Next    string
}
type Lister interface {
	List(context.Context, string, string) (DirectoryPage, error)
}
type InventoryScan struct {
	ID, SessionID, Owner, EnvironmentID    string
	Fence, EventSequence, MutationSequence int64
	Reference                              sandbox.Reference
	StartedAt                              time.Time
}
type InventoryRepository interface {
	WorkspaceInventorySessions(context.Context, string, int) ([]string, error)
	BeginWorkspaceInventory(context.Context, string, string, string) (InventoryScan, error)
	CommitWorkspaceInventory(context.Context, InventoryScan, []File) ([]int64, error)
}

// Collect produces a complete bounded observation before persistence. Live tree
// changes may require another scan; a failed/overflowed walk is never a deletion
// claim. Symlinks, special files and incomplete upload staging are not artifacts.
func Collect(ctx context.Context, source Lister, limit int) ([]File, error) {
	if source == nil || limit <= 0 {
		return nil, fmt.Errorf("inventory source and positive entry bound required")
	}
	queue := []string{""}
	var result []File
	seen := 0
	pages := 0
	for len(queue) > 0 {
		directory := queue[0]
		queue = queue[1:]
		after := ""
		for {
			pages++
			if pages > limit+1 {
				return nil, fmt.Errorf("workspace inventory exceeds page bound")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, err := source.List(ctx, directory, after)
			if err != nil {
				return nil, err
			}
			last := after
			for _, entry := range page.Entries {
				if entry.Name <= last || strings.Contains(entry.Name, "/") {
					return nil, fmt.Errorf("unordered or invalid directory entry")
				}
				if _, err := CleanPath(entry.Name); err != nil {
					return nil, err
				}
				last = entry.Name
				seen++
				if seen > limit {
					return nil, fmt.Errorf("workspace inventory exceeds entry bound")
				}
				if strings.HasPrefix(entry.Name, ".harness-upload-") {
					continue
				}
				path := entry.Name
				if directory != "" {
					path = directory + "/" + path
				}
				if len(path) > 4096 || strings.Count(path, "/") >= 64 {
					return nil, fmt.Errorf("workspace inventory path/depth limit exceeded")
				}
				switch entry.Type {
				case "directory":
					queue = append(queue, path)
				case "file":
					if entry.Size < 0 || entry.ModifiedAt.IsZero() {
						return nil, fmt.Errorf("invalid artifact metadata")
					}
					result = append(result, File{Path: path, Size: entry.Size, ModifiedAt: entry.ModifiedAt})
				case "symlink", "other":
				default:
					return nil, fmt.Errorf("unknown workspace entry type")
				}
			}
			if page.Next == "" {
				break
			}
			if page.Next <= after || page.Next < last {
				return nil, fmt.Errorf("directory pagination made no valid progress")
			}
			after = page.Next
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}
