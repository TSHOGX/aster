package execution

import (
	"context"
	"fmt"
	"time"
)

type TurnCommand struct {
	ID, SessionID, GenerationID, Owner, Kind, Message string
	TurnID                                            int64
	Acknowledged                                      bool
}
type TurnCommandRepository interface {
	ReserveTurnCommand(context.Context, TurnCommand) (TurnCommand, error)
	AcknowledgeTurnCommand(context.Context, Binding, string) error
	TurnCommandAcknowledged(context.Context, TurnCommand) (bool, error)
	TurnCommandGrantIDs(context.Context, TurnCommand) ([]string, error)
}
type GrantRevoker interface {
	RevokeGrant(context.Context, string) error
}
type TurnCommandSender interface {
	SendTurnCommand(context.Context, TurnCommand) error
}

func (c *Controller) DeliverTurnCommand(ctx context.Context, p TurnCommand) error {
	if c.Repository == nil || c.Lifetime == nil {
		return fmt.Errorf("turn command controller ports required")
	}
	deadline, cancelDeadline := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDeadline()
	ctx = deadline
	worker, lifetime, err := c.session(p.SessionID)
	if err != nil {
		return err
	}
	request, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	ctx = request
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.connection == nil || worker.binding.GenerationID != p.GenerationID || worker.binding.Owner != p.Owner {
		return fmt.Errorf("turn command requires attached execution")
	}
	select {
	case <-worker.connection.Done():
		return fmt.Errorf("turn command stream ended")
	default:
	}
	sender, ok := worker.connection.(TurnCommandSender)
	if !ok {
		return fmt.Errorf("agent connection does not support turn commands")
	}
	command, err := c.Repository.ReserveTurnCommand(ctx, p)
	if err != nil {
		return err
	}
	if !command.Acknowledged {
		if err := sender.SendTurnCommand(ctx, command); err != nil {
			return err
		}
	}
	if command.Kind == "abort" {
		grants, err := c.Repository.TurnCommandGrantIDs(ctx, command)
		if err != nil {
			return err
		}
		if len(grants) > 0 && c.GrantRevoker == nil {
			return fmt.Errorf("turn interrupt requires gateway grant revocation")
		}
		for _, grant := range grants {
			if err := c.GrantRevoker.RevokeGrant(ctx, grant); err != nil {
				return err
			}
		}
	}
	if command.Acknowledged {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		ack, err := c.Repository.TurnCommandAcknowledged(ctx, command)
		if err != nil {
			return err
		}
		if ack {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-worker.connection.Done():
			return fmt.Errorf("turn command delivery unresolved after detach")
		case <-ticker.C:
		}
	}
}
