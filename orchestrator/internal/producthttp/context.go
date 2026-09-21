package producthttp

import "context"

type actorKey struct{}

// withActor carries the authenticated identity that guard resolved. Handlers read
// it from the context rather than from anything the client sent, so a request
// body or path can never nominate its own actor.
func withActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func actorFrom(ctx context.Context) string {
	actor, _ := ctx.Value(actorKey{}).(string)
	return actor
}
