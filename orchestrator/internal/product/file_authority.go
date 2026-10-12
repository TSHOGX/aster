package product

import (
	"context"
	"io"

	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

func validFileMutation(action ProductAction, request projectfiles.Request) bool {
	if request.Validate() != nil {
		return false
	}
	switch action {
	case ActionFilesUpload, ActionFilesEdit:
		return request.Operation == "write"
	case ActionFilesManage:
		return request.Operation == "mkdir" || request.Operation == "move" || request.Operation == "remove"
	}
	return false
}

// Routes select these actions on the server. All mutations share confinement,
// transfer bounds and durable operation identity, including the selected action.
func (f *Files) Upload(ctx context.Context, scope Scope, id string, request projectfiles.Request, input io.Reader) (FileOperation, error) {
	return f.execute(ctx, scope, id, ActionFilesUpload, request, input)
}
func (f *Files) Edit(ctx context.Context, scope Scope, id string, request projectfiles.Request, input io.Reader) (FileOperation, error) {
	return f.execute(ctx, scope, id, ActionFilesEdit, request, input)
}
func (f *Files) Manage(ctx context.Context, scope Scope, id string, request projectfiles.Request) (FileOperation, error) {
	return f.execute(ctx, scope, id, ActionFilesManage, request, nil)
}
func (f *Files) Download(ctx context.Context, scope Scope, path string) (*CapturedFile, error) {
	if _, err := projectAction(ctx, f.Store.reader, scope, ActionFilesExport); err != nil {
		return nil, err
	}
	capture, err := f.Capture(ctx, scope, path)
	if err != nil {
		return nil, err
	}
	if _, err = projectAction(ctx, f.Store.reader, scope, ActionFilesExport); err != nil {
		_ = capture.Close()
		return nil, err
	}
	return capture, nil
}

// The shared resource stream may retain its own Project-read authority after
// file-list revocation. Recheck each file event before exposing workspace names.
type authorizedFileWatch struct {
	sandbox.FileWatch
	ctx   context.Context
	store *Store
	scope Scope
}

func (w *authorizedFileWatch) Next() (sandbox.FileInvalidation, error) {
	event, err := w.FileWatch.Next()
	if err != nil {
		return event, err
	}
	if _, err = projectAction(w.ctx, w.store.reader, w.scope, ActionFilesList); err != nil {
		return sandbox.FileInvalidation{}, err
	}
	if fileMount(w.ctx) != "" {
		r, err := w.store.Resources(w.ctx, w.scope)
		if err != nil {
			return sandbox.FileInvalidation{}, err
		}
		if _, err = fileResourceView(w.ctx, w.store.reader, w.scope, r, false); err != nil {
			return sandbox.FileInvalidation{}, err
		}
	}
	return event, nil
}
