// Command preview-worker derives an Office reading model inside the preview
// process sandbox. It has no application database, engine access or Agent role.
package main

import (
	"context"
	"fmt"
	"os"

	"harness-platform/orchestrator/internal/previewmodel"
)

func main() {
	if len(os.Args) != 4 {
		os.Exit(2)
	}
	if err := previewmodel.Build(context.Background(), os.Args[1], os.Args[2], os.Args[3]); err != nil {
		// Expose only the approved code; parser text and paths stay private.
		code := "renderer_failed"
		if err == previewmodel.ErrInvalid || err == previewmodel.ErrLimit || err == previewmodel.ErrUnsupported {
			code = err.Error()
		}
		fmt.Fprintln(os.Stdout, code)
		os.Exit(1)
	}
}
