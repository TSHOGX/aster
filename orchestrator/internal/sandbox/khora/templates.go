package khora

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
)

func (b *Backend) RequireTemplate(ctx context.Context, id string) error {
	if id == "" || len(id) > 128 || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#\x00") {
		return failure("template", sandbox.Invalid, fmt.Errorf("invalid template identity"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return err
	}
	template, err := b.client.Template(ctx, id)
	if err != nil {
		return mapError("template", err)
	}
	if template.TemplateID != id {
		return failure("template", sandbox.Conflict, fmt.Errorf("template identity mismatch"))
	}
	return nil
}
