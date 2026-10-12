//go:build khoraintegration

package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/testfixture"
)

func fixtureRuntimeOnKhora(t *testing.T, ctx context.Context, store *Store, backend content.Backend, profile Profile, entries ...map[string][]content.Entry) Profile {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("PROJECT_TEST_MANIFEST"))
	must(t, err)
	var manifest runtimerelease.Manifest
	must(t, json.Unmarshal(raw, &manifest))
	sources := map[string][]content.Entry{}
	if len(entries) > 0 {
		for digest, value := range entries[0] {
			sources[digest] = value
		}
	}
	required := []runtimerelease.RequiredPublication{}
	for _, role := range manifest.Mounts.RequiredPublications {
		if _, ok := profile.VolumeForRole(role.Role); !ok {
			required = append(required, role)
		}
	}
	publications, err := testfixture.RuntimePublications(ctx, backend, required)
	must(t, err)
	records := []content.Publication{}
	for _, item := range publications {
		p := item.Publication
		records = append(records, p)
		sources[p.RevisionDigest] = p.Entries
		profile.Volumes = append(profile.Volumes, VolumePlan{Key: item.Required.Role, Role: item.Required.Role, Destination: item.Required.Destination, Storage: "shared_readonly", Access: "read_only", CapacityBytes: p.Capacity, Source: &VolumeSource{ContentProfile: "reviewed-v1", Reference: ArtifactReference{ID: item.Required.ArtifactID, Version: p.RevisionDigest, Digest: p.RevisionDigest}, VolumeID: p.PublishedVolumeID, MinimumBytes: p.Capacity}})
	}
	must(t, store.write(ctx, func(tx *sql.Tx) error { return testfixture.RecordRuntimePublications(ctx, tx, records) }))
	fixtureEnvironment(t, store, profile, sources)
	return profile
}
