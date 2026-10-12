package product

import (
	"context"
	"time"
)

// Service observers, including callers outside HTTP, lose interactive access
// after a committed policy change. Closing a view never signals its retained PTY.
func (t *Terminals) watchObserver(o *TerminalObserver, terminal string) {
	defer o.Close()
	changes := t.Store.IdentityInvalidations()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		check, cancel := context.WithTimeout(t.ctx, time.Second)
		err := t.Store.CanUseTerminal(check, o.scope, terminal)
		cancel()
		if err != nil {
			return
		}
		select {
		case <-t.ctx.Done():
			return
		case <-o.done:
			return
		case <-changes:
			changes = t.Store.IdentityInvalidations()
		case <-tick.C:
		}
	}
}
