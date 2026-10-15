package projections

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"fmt"
)

// ValidateConfiguration resolves exact catalog pins in one database snapshot.
// Resource access and central operation authority are checked separately.
func (s *Store) ValidateConfiguration(ctx context.Context, config evolution.Configuration) error {
	if err := config.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return validateConfigurationCatalog(ctx, tx, config)
}
func validateConfigurationCatalog(ctx context.Context, tx *sql.Tx, config evolution.Configuration) error {
	resolver := workflowResolver{tx}
	for _, binding := range config.Bindings {
		workflow, err := resolver.ResolveWorkflow(ctx, binding.Workflow)
		if err != nil {
			return fmt.Errorf("binding %s workflow: %w", binding.ID, err)
		}
		if err = binding.ValidateInputPorts(workflow); err != nil {
			return err
		}
		definition, err := readDefinition(ctx, tx, "template", binding.Template)
		if err != nil {
			return fmt.Errorf("binding %s template: %w", binding.ID, err)
		}
		template := definition.(evolution.Template)
		if err = validateParameters(template.ParametersSchema, binding.Parameters); err != nil {
			return fmt.Errorf("binding %s template parameters: %w", binding.ID, err)
		}
		if err = validateParameters(workflow.ParametersSchema, binding.Parameters); err != nil {
			return fmt.Errorf("binding %s workflow parameters: %w", binding.ID, err)
		}
		if err = template.SupportsWorkflow(binding.Workflow, workflow, binding.Output.Profile); err != nil {
			return err
		}
		definition, err = readDefinition(ctx, tx, "service", binding.Service)
		if err != nil {
			return fmt.Errorf("binding %s service: %w", binding.ID, err)
		}
		service := definition.(evolution.Service)
		if err = service.SupportsClosure(ctx, workflow, resolver); err != nil {
			return err
		}
		if binding.Evaluation.Mode == "disabled" {
			continue
		}
		definition, err = readDefinition(ctx, tx, "profile", *binding.Evaluation.Profile)
		if err != nil {
			return fmt.Errorf("binding %s evaluation profile: %w", binding.ID, err)
		}
		profile := definition.(evolution.EvaluationProfile)
		definition, err = readDefinition(ctx, tx, "service", *binding.Evaluation.Service)
		if err != nil {
			return fmt.Errorf("binding %s evaluation service: %w", binding.ID, err)
		}
		evaluator := definition.(evolution.Service)
		if !evaluator.Enabled || evaluator.Kind != "evaluation" {
			return fmt.Errorf("enabled evaluation service required")
		}
		if profile.FixedModelRequired && !evaluator.Capabilities.VersionedModels {
			return fmt.Errorf("evaluation requires versioned models")
		}
	}
	return nil
}
