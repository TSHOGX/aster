package authoring

import (
	"context"
	"time"
)

// settleTimeout bounds the Git steps that follow a committed durable intent.
const settleTimeout = 2 * time.Minute

// settle runs a step that must finish once its intent is committed. It is
// detached from the request, so a client that disconnects mid-request cannot
// cancel it and leave the store poisoned until restart.
func settle(ctx context.Context, step func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	return step(ctx)
}
