package application

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
)

// VolumePublication is trusted operator input on the local protected socket.
// Source names a reviewed directory readable by the owner, never browser input.
type VolumePublication struct {
	Reference     product.ArtifactReference `json:"reference"`
	Source        string                    `json:"source"`
	CapacityBytes int64                     `json:"capacity_bytes,string"`
	MinimumBytes  int64                     `json:"minimum_bytes,string"`
	Roles         []string                  `json:"roles"`
	CleanRuntime  bool                      `json:"clean_runtime"`
}

func (input VolumePublication) prepare(storeID string) (product.BuiltinVolume, content.Revision, error) {
	var artifact product.BuiltinVolume
	if !filepath.IsAbs(input.Source) || input.Source == "/" || input.CapacityBytes <= 0 || input.MinimumBytes <= 0 || input.MinimumBytes > input.CapacityBytes {
		return artifact, content.Revision{}, product.ErrInvalid
	}
	revision, err := content.Read(input.Source)
	if err != nil {
		return artifact, revision, err
	}
	if revision.Digest != input.Reference.Digest {
		return artifact, revision, fmt.Errorf("reviewed source digest differs from requested artifact")
	}
	// Include bounded filesystem metadata overhead in the minimum advertised for
	// independent copies, using the same lower bound as publication itself.
	if input.MinimumBytes < revision.TotalBytes+int64(len(revision.Entries))*(16<<10) {
		return artifact, revision, fmt.Errorf("minimum capacity is below the bounded content footprint")
	}
	artifact = product.BuiltinVolume{Reference: input.Reference, StoreID: storeID, PublicationPurpose: "builtin", MinimumBytes: input.MinimumBytes, Roles: input.Roles, CleanRuntime: input.CleanRuntime, Entries: revision.Entries}
	return artifact, revision, product.ValidateBuiltinVolume(artifact)
}

func (a *Application) publishVolume(w http.ResponseWriter, r *http.Request) {
	var input VolumePublication
	if !decodeControl(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	a.publication.Lock()
	defer a.publication.Unlock()
	artifact, revision, err := input.prepare(a.Config.Engine.StoreID)
	if err == nil {
		err = a.Store.CheckBuiltinRegistration(ctx, artifact)
	}
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	publisher := content.Publisher{Repository: a.Store, Backend: a.Backend}
	volume, err := publisher.Publish(ctx, artifact.PublicationPurpose, revision, input.CapacityBytes)
	if err == nil {
		// A publication replay cannot turn an externally deleted dependency into
		// successful registration. Never recreate or remap its committed binding.
		_, err = a.Backend.InspectVolume(ctx, sandbox.VolumeReference{StoreID: artifact.StoreID, VolumeID: volume})
	}
	if err == nil {
		err = a.Store.RegisterBuiltinVolume(ctx, artifact)
	}
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeControl(w, map[string]any{"status": "registered", "reference": artifact.Reference, "default_changed": false})
}
