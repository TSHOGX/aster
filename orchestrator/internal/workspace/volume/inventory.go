package volume

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"sort"
)

func (f Files) Inventory(ctx context.Context, limit int) ([]workspace.File, error) {
	listings, ok := f.Backend.(sandbox.VolumeListings)
	if !ok || f.Repository == nil {
		return nil, fmt.Errorf("offline inventory ports required")
	}
	lease, err := f.Repository.BeginOfflineWorkspaceRead(ctx, f.SessionID, f.Owner, uuid.NewString())
	if err != nil {
		return nil, err
	}
	reader := &reader{ctx: ctx, files: f, lease: lease}
	defer reader.Close()
	observed, err := f.Backend.Inspect(ctx, lease.Reference)
	if err != nil {
		return nil, err
	}
	if observed.Reference != lease.Reference || observed.State != "paused" || observed.PlacementState != "lost" {
		return nil, workspace.ErrMutationBusy
	}
	return workspace.Collect(ctx, volumeLister{backend: listings, reference: lease.Volume}, limit)
}

type volumeLister struct {
	backend   sandbox.VolumeListings
	reference sandbox.VolumeReference
}

func (l volumeLister) List(ctx context.Context, path, after string) (workspace.DirectoryPage, error) {
	if after != "" {
		return workspace.DirectoryPage{}, fmt.Errorf("unexpected volume pagination token")
	}
	entries, err := l.backend.ListVolume(ctx, l.reference, path)
	if err != nil {
		return workspace.DirectoryPage{}, err
	}
	page := workspace.DirectoryPage{}
	for _, entry := range entries {
		page.Entries = append(page.Entries, workspace.Entry{Name: entry.Name, Type: entry.Type, Size: entry.Size, ModifiedAt: entry.ModifiedAt})
	}
	sort.Slice(page.Entries, func(i, j int) bool { return page.Entries[i].Name < page.Entries[j].Name })
	return page, nil
}
