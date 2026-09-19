package bridge

import (
	"context"
	"fmt"
	"strconv"
)

// ProcessOnce binds the local file carrier to an execution selected by the host.
// Protocol processing itself uses Transport and has no queue-path dependency.
func (p *Processor) ProcessOnce(ctx context.Context, root string, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	inbox, err := OpenQueue(root, InboxDir)
	if err != nil {
		return err
	}
	outbox, err := OpenQueue(root, OutboxDir)
	if err != nil {
		return err
	}
	return p.Process(ctx, scope, &fileTransport{inbox: inbox, outbox: outbox})
}

type fileTransport struct {
	inbox, outbox Queue
	receipts      map[string]MessageFile
}

func (t *fileTransport) Receive(ctx context.Context) ([]Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := t.outbox.ReadAll()
	if err != nil {
		return nil, err
	}
	t.receipts = make(map[string]MessageFile, len(files))
	deliveries := make([]Delivery, 0, len(files))
	for _, file := range files {
		receipt := strconv.FormatUint(file.Seq, 10)
		t.receipts[receipt] = file
		deliveries = append(deliveries, Delivery{Receipt: receipt, Envelope: file.Envelope})
	}
	return deliveries, nil
}
func (t *fileTransport) Send(ctx context.Context, envelope Envelope) error {
	_, err := t.inbox.Write(ctx, envelope)
	return err
}
func (t *fileTransport) Acknowledge(ctx context.Context, receipt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, ok := t.receipts[receipt]
	if !ok {
		return fmt.Errorf("unknown bridge delivery receipt")
	}
	if err := file.Unlink(); err != nil {
		return err
	}
	delete(t.receipts, receipt)
	return nil
}
