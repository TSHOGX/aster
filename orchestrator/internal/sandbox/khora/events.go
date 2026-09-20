package khora

import (
	"context"
	"fmt"

	"harness-platform/orchestrator/internal/sandbox"

	sdk "github.com/TSHOGX/khora/pkg/khora"
)

func (b *Backend) Events(ctx context.Context, after int64, limit int) (sandbox.EventPage, error) {
	if after < 0 || limit < 1 || limit > 1024 {
		return sandbox.EventPage{}, failure("events", sandbox.Invalid, fmt.Errorf("bounded event cursor and page required"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.EventPage{}, err
	}
	page, err := b.client.Events(ctx, sdk.EventsOptions{After: after, Limit: limit})
	mapped := mapError("events", err)
	if sandbox.Is(mapped, sandbox.CursorExpired) {
		// The SDK discards the typed 410 body. Read a current typed window before
		// the caller scans inventory; never parse the human error text for bounds.
		window, windowErr := b.client.Events(ctx, sdk.EventsOptions{After: 0, Limit: 1})
		if windowErr != nil {
			return sandbox.EventPage{}, mapError("event window", windowErr)
		}
		if _, err := b.Identity(ctx); err != nil {
			return sandbox.EventPage{}, err
		}
		translated := eventPage(b.identity.StoreID, window)
		return translated, &sandbox.EventWindowExpired{Page: translated, Cause: mapped}
	}
	if err != nil {
		return sandbox.EventPage{}, mapped
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.EventPage{}, err
	}
	return eventPage(b.identity.StoreID, page), nil
}
func eventPage(identity string, page sdk.EventPage) sandbox.EventPage {
	result := sandbox.EventPage{StoreID: identity, Earliest: page.Earliest, Latest: page.Latest}
	for _, event := range page.Events {
		result.Events = append(result.Events, sandbox.Event{Sequence: event.Seq, ID: event.ID, SandboxID: event.SandboxID, Kind: string(event.Kind), State: string(event.State), Epoch: event.PlacementEpoch, Address: event.PlacementAddress, Cause: event.Cause, OccurredAt: event.OccurredAt})
	}
	return result
}
