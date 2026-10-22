package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/volumeimport"
)

type VolumeImportConfig struct {
	Sources       []volumeimport.Source `json:"sources"`
	TransferBytes int64                 `json:"transfer_bytes,string"`
	EngineBytes   int64                 `json:"engine_bytes,string"`
}

func (c *VolumeImportConfig) Validate() error {
	if c == nil {
		return nil
	}
	if len(c.Sources) == 0 || len(c.Sources) > 32 || c.TransferBytes < 1<<30 || c.EngineBytes < 128<<20 {
		return fmt.Errorf("volume imports require bounded sources, transfer and engine budgets")
	}
	seen := map[string]bool{}
	for _, source := range c.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if seen[source.ID] {
			return fmt.Errorf("duplicate volume source")
		}
		seen[source.ID] = true
	}
	return nil
}
func (a *Application) volumeSource(id string) (volumeimport.Source, error) {
	if a.Config.VolumeImports != nil {
		for _, source := range a.Config.VolumeImports.Sources {
			if source.ID == id {
				return source, nil
			}
		}
	}
	return volumeimport.Source{}, product.ErrNotFound
}
func (a *Application) volumeImportHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/volume-sources", func(w http.ResponseWriter, r *http.Request) {
		sources := []map[string]string{}
		if a.Config.VolumeImports != nil {
			for _, source := range a.Config.VolumeImports.Sources {
				sources = append(sources, map[string]string{"id": source.ID, "publisher_id": source.PublisherID})
			}
		}
		importJSON(w, 200, map[string]any{"sources": sources})
	})
	mux.HandleFunc("GET /api/admin/volume-sources/{source}/libraries", func(w http.ResponseWriter, r *http.Request) {
		source, err := a.volumeSource(r.PathValue("source"))
		if err != nil {
			importError(w, err)
			return
		}
		client, err := volumeimport.NewConnector(source)
		if err != nil {
			importError(w, err)
			return
		}
		libraries, next, err := client.Libraries(r.Context(), r.URL.Query().Get("after"))
		if err != nil {
			importError(w, err)
			return
		}
		importJSON(w, 200, map[string]any{"libraries": libraries, "next": next})
	})
	mux.HandleFunc("GET /api/admin/volume-sources/{source}/releases", func(w http.ResponseWriter, r *http.Request) {
		source, err := a.volumeSource(r.PathValue("source"))
		if err != nil {
			importError(w, err)
			return
		}
		client, err := volumeimport.NewConnector(source)
		if err != nil {
			importError(w, err)
			return
		}
		versions, next, err := client.Releases(r.Context(), r.URL.Query().Get("library_id"), r.URL.Query().Get("after"))
		if err != nil {
			importError(w, err)
			return
		}
		importJSON(w, 200, map[string]any{"releases": versions, "next": next})
	})
	mux.HandleFunc("POST /api/admin/volume-imports", func(w http.ResponseWriter, r *http.Request) {
		var input product.VolumeImportInput
		if !importDecode(w, r, &input) {
			return
		}
		source, err := a.volumeSource(input.SourceID)
		if err != nil {
			importError(w, err)
			return
		}
		// A current authorized receipt is resolved before touching the source. Lost
		// admission replies remain replayable even if Studio subsequently goes down.
		if existing, found, err := a.Store.VolumeImportReceipt(r.Context(), importToken(r), input); err != nil {
			importError(w, err)
			return
		} else if found {
			importJSON(w, 202, existing)
			return
		}
		client, err := volumeimport.NewConnector(source)
		if err != nil {
			importError(w, err)
			return
		}
		descriptor, raw, err := client.Manifest(r.Context(), input.VersionID, input.ExpectedManifestDigest)
		if err != nil {
			importError(w, err)
			return
		}
		if descriptor.Profile != input.ConsumerProfile {
			importError(w, product.ErrInvalid)
			return
		}
		publisherActor := ""
		labels := product.VolumeImportLabels{LibraryName: descriptor.LibraryID}
		cursor := ""
		for pages := 0; pages < 1000; pages++ {
			releases, next, err := client.Releases(r.Context(), descriptor.LibraryID, cursor)
			if err != nil {
				importError(w, err)
				return
			}
			for _, release := range releases {
				if release.ID == descriptor.VersionID {
					if release.Profile != descriptor.Profile || release.DescriptorDigest != input.ExpectedManifestDigest {
						importError(w, product.ErrConflict)
						return
					}
					publisherActor = release.Publisher.Issuer + "/" + release.Publisher.Subject
					labels.VersionLabel = release.Label
					labels.VersionNote = release.Note
				}
			}
			if publisherActor != "" || next == "" {
				break
			}
			if next == cursor {
				importError(w, product.ErrConflict)
				return
			}
			cursor = next
		}
		if publisherActor == "" {
			importError(w, product.ErrArtifactUnavailable)
			return
		}
		// Display metadata is captured locally; it is never runtime authority.
		labelsCtx, labelsDone := context.WithTimeout(r.Context(), 5*time.Second)
		defer labelsDone()
		for cursor, pages := "", 0; pages < 100; pages++ {
			libraries, next, e := client.Libraries(labelsCtx, cursor)
			if e != nil {
				break
			}
			found := false
			for _, library := range libraries {
				if library.ID == descriptor.LibraryID {
					labels.LibraryName = library.Name
					found = true
					break
				}
			}
			if found || next == "" || next == cursor {
				break
			}
			cursor = next
		}

		a.publication.Lock()
		defer a.publication.Unlock()
		imported, err := a.Store.AdmitVolumeImport(r.Context(), importToken(r), input, raw, a.Config.Engine.StoreID, publisherActor, a.Config.VolumeImports.TransferBytes, a.Config.VolumeImports.EngineBytes, labels)
		if err != nil {
			importError(w, err)
			return
		}
		importJSON(w, 202, imported)
	})
	mux.HandleFunc("GET /api/admin/volume-imports", func(w http.ResponseWriter, r *http.Request) {
		imports, err := a.Store.VolumeImports(r.Context(), r.URL.Query().Get("after"))
		if err != nil {
			importError(w, err)
			return
		}
		importJSON(w, 200, map[string]any{"imports": imports})
	})
	mux.HandleFunc("GET /api/admin/volume-imports/{id}", func(w http.ResponseWriter, r *http.Request) {
		i, err := a.Store.VolumeImport(r.Context(), r.PathValue("id"))
		if err != nil {
			importError(w, err)
			return
		}
		importJSON(w, 200, i)
	})
	mux.HandleFunc("POST /api/admin/volume-imports/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RequestID string `json:"request_id"`
		}
		if !importDecode(w, r, &input) {
			return
		}
		// Retry/cancel serialize with local publication; a canceled remote write is
		// never retried on the same staging object. Durable state decides finalization.
		a.publication.Lock()
		defer a.publication.Unlock()
		action := r.PathValue("action")
		id := r.PathValue("id")
		if action == "retire" {
			i, err := a.Store.RetireImportedVolume(r.Context(), importToken(r), id, input.RequestID)
			if err != nil {
				importError(w, err)
				return
			}
			importJSON(w, 202, i)
			return
		}

		if action == "retry" {
			if existing, found, err := a.Store.VolumeImportActionReceipt(r.Context(), importToken(r), id, input.RequestID, action); err != nil {
				importError(w, err)
				return
			} else if found {
				importJSON(w, 200, existing)
				return
			}
			i, err := a.Store.VolumeImport(r.Context(), id)
			if err != nil {
				importError(w, err)
				return
			}
			if a.Config.VolumeImports == nil {
				importError(w, product.ErrInvalid)
				return
			}
			if err = a.Store.RetryImportedPublication(r.Context(), i, a.Config.VolumeImports.EngineBytes); err != nil {
				importError(w, err)
				return
			}
		}
		i, err := a.Store.VolumeImportAction(r.Context(), importToken(r), id, input.RequestID, action)
		if err != nil {
			importError(w, err)
			return
		}
		importJSON(w, 200, i)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.Store.VolumeImportAdmin(r.Context(), importToken(r)); err != nil {
			importError(w, err)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func importToken(r *http.Request) string {
	c, err := r.Cookie("project_login")
	if err != nil {
		return ""
	}
	return c.Value
}
func importDecode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil || d.Decode(new(any)) != io.EOF {
		importJSON(w, 400, map[string]string{"error": "invalid import request"})
		return false
	}
	return true
}
func importJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func importError(w http.ResponseWriter, err error) {
	status, code := 503, "source_or_runtime_unavailable"
	switch {
	case errors.Is(err, product.ErrUnauthorized):
		status, code = 401, "login_required"
	case errors.Is(err, product.ErrForbidden):
		status, code = 403, "admin_required"
	case errors.Is(err, product.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, product.ErrInvalid):
		status, code = 422, "invalid_import"
	case errors.Is(err, product.ErrConflict):
		status, code = 409, "import_conflict"
	case errors.Is(err, product.ErrCapacity):
		status, code = 507, "import_budget_exhausted"
	}
	importJSON(w, status, map[string]string{"error": code})
}
func (a *Application) runVolumeImports(ctx context.Context) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.publication.Lock()
			if cleanup, err := a.Store.NextImportCleanup(ctx); err == nil {
				if err = a.cleanupVolumeImport(ctx, cleanup); err != nil {
					code := "cleanup_requires_reconciliation"
					if sandbox.Is(err, sandbox.Conflict) {
						code = "engine_descendants_or_writer_block_cleanup"
					}
					_ = a.Store.ImportCleanupError(ctx, cleanup.ID, code)
				}
			}
			i, err := a.Store.NextVolumeImport(ctx)
			if err == nil {
				err = a.performVolumeImport(ctx, i)
				if err != nil && !errors.Is(err, context.Canceled) {
					_ = a.Store.SetVolumeImportError(ctx, i.ID, "source_or_runtime_unavailable")
				}
			}
			a.publication.Unlock()
		}
	}
}
func (a *Application) performVolumeImport(ctx context.Context, i product.VolumeImport) error {
	if err := a.Store.AuthorizeVolumeImport(ctx, i.ActorID); err != nil {
		return a.Store.AdvanceVolumeImport(ctx, i.ID, i.State, "paused", "principal_authority_required")
	}
	root := filepath.Join(a.Config.StateDirectory, "volume-transfers", i.ID)
	cache, err := volumeimport.Admit(root, volumeimport.Identity{ID: i.ID, SourceID: i.SourceID, PublisherID: i.PublisherID, VersionID: i.VersionID, DescriptorDigest: i.ManifestDigest, Profile: i.Profile}, i.Manifest)
	if err != nil {
		return err
	}
	defer cache.Close()
	if i.State == "acquiring" {
		if cache.Status().State != volumeimport.SourceAcquired {
			source, err := a.volumeSource(i.SourceID)
			if err != nil {
				return err
			}
			client, err := volumeimport.NewConnector(source)
			if err != nil {
				return err
			}
			if err = client.Acquire(ctx, cache); err != nil {
				if errors.Is(err, volumeimport.ErrQuarantined) || errors.Is(err, volumeimport.ErrIdentityConflict) {
					_ = a.Store.AdvanceVolumeImport(ctx, i.ID, i.State, "failed", "source_integrity_failed")
				}
				return err
			}
		}
		if err = a.Store.AdvanceVolumeImport(ctx, i.ID, "acquiring", "source_acquired", ""); err != nil {
			return err
		}
		i.State = "source_acquired"
	}
	if i.State == "source_acquired" {
		if err = a.Store.AdvanceVolumeImport(ctx, i.ID, i.State, "materializing", ""); err != nil {
			return err
		}
		i.State = "materializing"
	}
	if cache.Status().State != volumeimport.SourceAcquired {
		return fmt.Errorf("verified local source unavailable")
	}
	if err = a.Store.AuthorizeVolumeImport(ctx, i.ActorID); err != nil {
		return a.Store.AdvanceVolumeImport(ctx, i.ID, i.State, "paused", "principal_authority_required")
	}
	revision, err := i.Revision(cache)
	if err != nil {
		return err
	}
	volume, err := (content.Publisher{Repository: a.Store, Backend: a.Backend, Authorize: func(ctx context.Context) error { return a.Store.AuthorizeVolumeImport(ctx, i.ActorID) }}).Publish(ctx, i.PublicationPurpose(), revision, i.Capacity)
	if err != nil {
		if errors.Is(err, product.ErrUnauthorized) || errors.Is(err, product.ErrForbidden) {
			return a.Store.AdvanceVolumeImport(ctx, i.ID, i.State, "paused", "principal_authority_required")
		}
		if errors.Is(err, content.ErrPublicationQuarantined) || errors.Is(err, content.ErrContentRefused) {
			_ = a.Store.AdvanceVolumeImport(ctx, i.ID, i.State, "failed", "materialization_requires_fresh_attempt")
		}
		return err
	}
	if _, err = a.Backend.InspectVolume(ctx, sandbox.VolumeReference{StoreID: i.StoreID, VolumeID: volume}); err != nil {
		return err
	}
	if err = a.Store.FinalizeVolumeImport(ctx, i.ID); err != nil {
		return err
	}
	if err = cache.Purge(); err != nil {
		return err
	}
	return a.Store.SettleVolumeTransfer(ctx, i.ID)
}

func (a *Application) cleanupVolumeImport(ctx context.Context, i product.VolumeImport) error {
	if i.State == "retiring" {
		p, remove, err := a.Store.PublicationCleanup(ctx, i)
		if err != nil {
			return err
		}
		if remove {
			// Replay keyed create/snapshot to recover identities whose admission reply
			// was lost. This may allocate an as-yet-uncreated reserved object solely to
			// establish its identity before deleting it; quota remains held throughout.
			if p.StagingVolumeID == "" {
				volume, err := a.Backend.CreateVolume(ctx, fmt.Sprintf("content-staging:%s:%s:g%d", p.Purpose, p.RevisionDigest, p.Generation), p.Capacity)
				if err != nil && !sandbox.Is(err, sandbox.NotFound) {
					return err
				}
				if volume.VolumeID == "" {
					return err
				}
				p.StagingVolumeID = volume.VolumeID
			}
			if p.DerivedVolumeID == "" && p.Stage == "populated" {
				volume, err := a.Backend.SnapshotVolume(ctx, sandbox.VolumeReference{StoreID: p.StoreID, VolumeID: p.StagingVolumeID}, fmt.Sprintf("content-publish:%s:%s:g%d", p.Purpose, p.RevisionDigest, p.Generation), p.Capacity)
				if err != nil && !sandbox.Is(err, sandbox.NotFound) {
					return err
				}
				if volume.VolumeID == "" {
					return err
				}
				p.DerivedVolumeID = volume.VolumeID
			}
			if err = a.Store.RecordCleanupObjects(ctx, p); err != nil {
				return err
			}
			if err = a.deletePublicationObjects(ctx, p); err != nil {
				return err
			}
			if err = a.Store.RemoveContentPublication(ctx, p); err != nil {
				return err
			}
		}
		attempts, err := a.Store.QuarantinedAttempts(ctx, i.StoreID, i.PublicationPurpose(), i.TreeDigest)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if err = a.deletePublicationObjects(ctx, attempt.Publication); err != nil {
				return err
			}
			if err = a.Store.SettleContentAttempt(ctx, attempt.Publication); err != nil {
				return err
			}
		}
		if err = a.Store.FinishImportRetirement(ctx, i.ID); err != nil {
			return err
		}
	}
	if err := volumeimport.PurgeCache(filepath.Join(a.Config.StateDirectory, "volume-transfers", i.ID)); err != nil {
		return err
	}
	return a.Store.SettleVolumeTransfer(ctx, i.ID)
}
func (a *Application) deletePublicationObjects(ctx context.Context, p content.Publication) error {
	// Engine descendant refusal is an observed blocker. Never delete an ancestor
	// first, invent absence after a timeout, or silently release its reservation.
	for _, id := range []string{p.DerivedVolumeID, p.StagingVolumeID} {
		if id == "" {
			continue
		}
		ref := sandbox.VolumeReference{StoreID: p.StoreID, VolumeID: id}
		_, err := a.Backend.InspectVolume(ctx, ref)
		if sandbox.Is(err, sandbox.NotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err = a.Backend.DeleteVolume(ctx, ref); err != nil && !sandbox.Is(err, sandbox.NotFound) {
			return err
		}
		if _, err = a.Backend.InspectVolume(ctx, ref); !sandbox.Is(err, sandbox.NotFound) {
			if err == nil {
				return product.ErrPending
			}
			return err
		}
	}
	return nil
}
