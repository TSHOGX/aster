package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"aster/contracts/evolution"
	"volume-studio/backend/internal/history"
)

// Active reservations point to the exact adopted configuration. Manual edits to
// these paths inherit the same checks as generated changes.
func (s *Store) currentBindingChecks(ctx context.Context, library string, branch Branch, main evolution.ContentRef, result history.Snapshot) ([]evolution.CheckRequirement, error) {
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return nil, err
	}
	mainTree, err := repo.Snapshot(ctx, main.CommitID)
	if err != nil {
		return nil, err
	}
	changes := compareEntries(mainTree.Entries, result.Entries)
	rows, err := s.db.QueryContext(ctx, `SELECT r.prefix,r.binding_id,r.generation,o.body,o.digest FROM evolution_prefix_reservations r JOIN evolution_reservation_operations o ON o.operation_id=r.operation_id WHERE r.library_id=? AND r.state='active' AND o.state='adopted' ORDER BY r.prefix`, library)
	if err != nil {
		return nil, err
	}
	type retained struct {
		prefix, binding string
		generation      int64
		body, digest    string
	}
	policies := []retained{}
	for rows.Next() {
		var p retained
		if err = rows.Scan(&p.prefix, &p.binding, &p.generation, &p.body, &p.digest); err != nil {
			rows.Close()
			return nil, err
		}
		policies = append(policies, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	checks := []evolution.CheckRequirement{}
	for _, p := range policies {
		touched := false
		for _, change := range changes {
			if prefixOverlap(p.prefix, change.Path) {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewBufferString(p.body))
		if err != nil || config.Validate() != nil || evolution.Digest([]byte(p.body)) != p.digest {
			return nil, ErrInvalid
		}
		found := false
		for _, binding := range config.Bindings {
			if binding.ID != p.binding {
				continue
			}
			if binding.Output.LibraryID != library || binding.Output.Prefix != p.prefix || binding.Output.ReservationGeneration != p.generation {
				return nil, ErrInvalid
			}
			found = true
			policy := binding.Evaluation
			if policy.Mode == "disabled" {
				break
			}
			policyID := "binding-" + strings.TrimPrefix(evolution.Digest([]byte(config.GroupID+"\x00"+binding.ID)), "sha256:")
			check := evolution.CheckRequirement{PolicyID: policyID, PolicyRevision: config.Revision, Path: p.prefix, Mode: policy.Mode, Profile: *policy.Profile, Service: *policy.Service, ReferenceStrategy: policy.ReferenceStrategy}
			switch policy.ReferenceStrategy {
			case "target_main":
				check.Reference = &main
			case "generation_base":
				tree, err := repo.Snapshot(ctx, branch.InitialBase)
				if err != nil {
					return nil, err
				}
				check.Reference = &evolution.ContentRef{LibraryID: library, CommitID: branch.InitialBase, Profile: tree.Profile, TreeDigest: tree.TreeDigest()}
			case "pinned_release":
				ref := policy.ReferenceRelease
				version, err := s.Version(ctx, ref.VersionID)
				if err != nil {
					return nil, err
				}
				if version.LibraryID != ref.LibraryID || version.PublisherID != ref.PublisherID || version.DescriptorDigest != ref.DescriptorDigest || version.AcceptedCommit == "" || version.State != "published" && version.State != "archived" {
					return nil, ErrConflict
				}
				referenceRepo, err := s.historyRepository(ctx, version.LibraryID)
				if err != nil {
					return nil, err
				}
				tree, err := referenceRepo.Snapshot(ctx, version.AcceptedCommit)
				if err != nil {
					return nil, err
				}
				check.Reference = &evolution.ContentRef{LibraryID: version.LibraryID, CommitID: version.AcceptedCommit, Profile: tree.Profile, TreeDigest: tree.TreeDigest()}
			}
			check.Digest, err = check.IdentityDigest()
			if err != nil || check.Validate() != nil {
				return nil, ErrInvalid
			}
			checks = append(checks, check)
			break
		}
		if !found {
			return nil, ErrInvalid
		}
	}
	return evolution.UnionChecks(checks)
}

// Requirements captured by an earlier preparation survive configuration
// relaxation. Repreparing against new main re-resolves only target-main controls.
func (s *Store) retainedBranchChecks(ctx context.Context, library, branch string, main evolution.ContentRef) ([]evolution.CheckRequirement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.identity,p.digest FROM prepared_merges p JOIN prepared_sources s ON s.prepared_digest=p.digest WHERE p.library_id=? AND s.branch_id=? ORDER BY p.created_at,p.digest`, library, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := []evolution.CheckRequirement{}
	for rows.Next() {
		var raw []byte
		var digest string
		if err = rows.Scan(&raw, &digest); err != nil {
			return nil, err
		}
		var prepared evolution.PreparedIdentity
		if json.Unmarshal(raw, &prepared) != nil {
			return nil, ErrInvalid
		}
		actual, err := prepared.ApprovalDigest()
		if err != nil || actual != digest {
			return nil, ErrInvalid
		}
		for _, check := range prepared.Checks {
			if check.ReferenceStrategy == "target_main" {
				check.Reference = &main
				check.Digest, err = check.IdentityDigest()
				if err != nil {
					return nil, err
				}
			}
			checks = append(checks, check)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return evolution.UnionChecks(checks)
}

func (s *Store) preparedBindingChecks(ctx context.Context, library string, branch Branch, main evolution.ContentRef, result history.Snapshot) ([]evolution.CheckRequirement, error) {
	current, err := s.currentBindingChecks(ctx, library, branch, main, result)
	if err != nil {
		return nil, err
	}
	retained, err := s.retainedBranchChecks(ctx, library, branch.ID, main)
	if err != nil {
		return nil, err
	}
	return evolution.UnionChecks(current, retained)
}
