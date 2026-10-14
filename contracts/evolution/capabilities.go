package evolution

import "fmt"

// SupportsWorkflow checks advertised execution support without flattening a graph
// into an opaque service. Health is advisory; disabled services cannot admit work.
func (s Service) SupportsWorkflow(w Workflow) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	if !s.Enabled || s.Kind != "evolution" {
		return fmt.Errorf("enabled evolution service required")
	}
	supported := false
	for _, version := range s.Capabilities.WorkflowVersions {
		if version == w.SchemaVersion {
			supported = true
		}
	}
	if !supported {
		return FieldError{"schema_version", "service does not support workflow version"}
	}
	kinds := map[string]bool{}
	for _, kind := range s.Capabilities.NodeKinds {
		kinds[kind] = true
	}
	for _, node := range w.Nodes {
		if !kinds[node.Kind] {
			return FieldError{"nodes." + node.ID + ".kind", "service does not support node kind"}
		}
	}
	capabilities := map[string]bool{"editable_graph": s.Capabilities.EditableGraph, "node_trace": s.Capabilities.NodeTrace, "effective_prompt": s.Capabilities.EffectivePrompt, "pause_resume": s.Capabilities.PauseResume, "fixture_dry_run": s.Capabilities.FixtureDryRun, "deterministic_transforms": s.Capabilities.DeterministicTransforms, "artifact_signatures": s.Capabilities.ArtifactSignatures, "versioned_models": s.Capabilities.VersionedModels}
	for _, required := range w.Capabilities {
		if !capabilities[required] {
			return FieldError{"capabilities", "service does not support required capability: " + required}
		}
	}
	return nil
}
