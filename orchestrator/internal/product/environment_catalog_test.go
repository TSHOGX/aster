package product

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func testEnvironment(ctx context.Context, q queryer) (DefaultEnvironment, []BuiltinVolume, error) {
	var environment DefaultEnvironment
	var raw string
	var revision int64
	if err := missing(q.QueryRowContext(ctx, `SELECT document,revision FROM environment_templates WHERE id IN ('fixture-environment','standard-agent') ORDER BY id LIMIT 1`).Scan(&raw, &revision)); err != nil {
		return environment, nil, err
	}
	var err error
	if environment, err = decodeEnvironment(raw, revision); err != nil {
		return environment, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT v.id,v.version,v.digest,v.store_id,v.publication_purpose,v.minimum_bytes,v.roles,v.clean_runtime,v.entries,coalesce((SELECT p.capacity FROM content_publications p WHERE p.store_id=v.store_id AND p.purpose=v.publication_purpose AND p.revision_digest=v.digest AND p.stage='published'),0) FROM builtin_volumes v WHERE EXISTS(SELECT 1 FROM environment_templates e,json_each(e.document,'$.mounts') m WHERE json_extract(m.value,'$.source.artifact_id')=v.id AND json_extract(m.value,'$.source.version')=v.version)`)
	if err != nil {
		return environment, nil, err
	}
	defer rows.Close()
	artifacts := []BuiltinVolume{}
	for rows.Next() {
		var artifact BuiltinVolume
		var roles, entries string
		if err = rows.Scan(&artifact.Reference.ID, &artifact.Reference.Version, &artifact.Reference.Digest, &artifact.StoreID, &artifact.PublicationPurpose, &artifact.MinimumBytes, &roles, &artifact.CleanRuntime, &entries, &artifact.SourceCapacityBytes); err != nil {
			return environment, nil, err
		}
		if err = json.Unmarshal([]byte(roles), &artifact.Roles); err != nil {
			return environment, nil, err
		}
		if err = json.Unmarshal([]byte(entries), &artifact.Entries); err != nil {
			return environment, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return environment, artifacts, rows.Err()
}

func saveFixtureEnvironment(t *testing.T, s *Store, token string, environment DefaultEnvironment) ResourceTemplate {
	t.Helper()
	var id string
	must(t, s.reader.QueryRow(`SELECT id FROM environment_templates WHERE id IN ('fixture-environment','standard-agent') ORDER BY id LIMIT 1`).Scan(&id))
	before, err := readTemplate(ctx, s.reader, "environment", id)
	must(t, err)
	return saveTemplateReviewed(t, s, token, "environment", id, TemplateEdit{RequestID: fmt.Sprintf("environment-%s-%d", id, before.Revision), ExpectedRevision: before.Revision, Name: before.Name, Description: before.Description, Enabled: before.Enabled, Environment: &environment})
}
