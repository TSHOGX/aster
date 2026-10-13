package khora

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
	"path"
	"regexp"
)

// InstallSessionHistory is a host-only, narrowly scoped bootstrap capability.
// It cannot write a live native session or any browser-selected workspace path.
func (b *Backend) InstallSessionHistory(ctx context.Context, ref sandbox.Reference, root, session string, content []byte) (string, error) {
	if !path.IsAbs(root) || path.Clean(root) != root || root == "/" || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`).MatchString(session) || len(content) > 64<<20 {
		return "", fmt.Errorf("invalid session history target")
	}
	if err := b.validateReference(ctx, ref, false); err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	name := path.Join(root, ".history-"+session+".jsonl")
	if err := b.client.WriteFile(ctx, ref.SandboxID, name, bytes.NewReader(content)); err != nil {
		return "", mapError("install session history", err)
	}
	reader, err := b.client.ReadFile(ctx, ref.SandboxID, name)
	if err != nil {
		return "", mapError("verify session history", err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(reader, int64(len(content))+1))
	closeErr := reader.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != int64(len(content)) || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return "", fmt.Errorf("session history verification failed")
	}
	if err = b.validateReference(ctx, ref, false); err != nil {
		return "", err
	}
	return digest, nil
}
