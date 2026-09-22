package projectfiles

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ServeReader consumes only read operations. Disconnect cancels all owned work;
// a request cancellation never closes another consumer's file or response.
// input.Close must unblock Read, as it does for a process stdin or an io.Pipe.
func ServeReader(ctx context.Context, root string, input io.ReadCloser, output io.Writer, idle time.Duration) error {
	reader, err := NewReader(root)
	if err != nil {
		return err
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closeInput := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer closeInput()
	if idle <= 0 {
		idle = ReaderIdleTimeout
	}
	var outputMu sync.Mutex
	send := func(frame ReadFrame) error {
		frame.Version = ReaderProtocol
		outputMu.Lock()
		defer outputMu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := WriteWire(output, ReaderFrameBytes, frame)
		if err != nil {
			cancel()
		}
		return err
	}
	if err = send(ReadFrame{Kind: "ready"}); err != nil {
		return err
	}
	var mu sync.Mutex
	active := map[string]context.CancelFunc{}
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	requests := make(chan ReadRequest)
	readDone := make(chan error, 1)
	go func() {
		for {
			var request ReadRequest
			e := ReadWire(input, ReaderRequestBytes, &request)
			if e != nil {
				readDone <- e
				return
			}
			select {
			case requests <- request:
			case <-ctx.Done():
				return
			}
		}
	}()
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readDone:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-timer.C:
			mu.Lock()
			count := len(active)
			mu.Unlock()
			if count == 0 {
				return nil
			}
			timer.Reset(idle)
		case request := <-requests:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
			if request.Validate() != nil {
				return ErrPath
			}
			mu.Lock()
			if request.Operation == "cancel" {
				if done := active[request.ID]; done != nil {
					done()
				}
				mu.Unlock()
				continue
			}
			if _, duplicate := active[request.ID]; duplicate {
				mu.Unlock()
				return ErrPath
			}
			if len(active) >= 2 {
				mu.Unlock()
				if err = send(ReadFrame{ID: request.ID, Kind: "error", Code: "capacity"}); err != nil {
					return err
				}
				continue
			}
			requestCtx, done := context.WithTimeout(ctx, ReadOperationTimeout)
			active[request.ID] = done
			mu.Unlock()
			workers.Add(1)
			go func(request ReadRequest) {
				defer workers.Done()
				defer done()
				defer func() { mu.Lock(); delete(active, request.ID); mu.Unlock() }()
				emit := func(frame ReadFrame) error {
					if requestCtx.Err() != nil {
						return requestCtx.Err()
					}
					frame.ID = request.ID
					return send(frame)
				}
				var err error
				switch request.Operation {
				case "read":
					err = reader.Read(requestCtx, request.Path, emit)
				case "stat":
					var meta ReadMetadata
					meta, err = reader.Stat(request.Path)
					if err == nil {
						err = emit(ReadFrame{Kind: "complete", Metadata: &meta})
					}
				case "list":
					var page Page
					page, err = reader.List(requestCtx, request.Path, request.Cursor, request.Limit)
					if err == nil {
						err = emit(ReadFrame{Kind: "complete", Page: &page})
					}
				}
				if err != nil {
					_ = send(ReadFrame{ID: request.ID, Kind: "error", Code: readErrorCode(err)})
				}
			}(request)
		}
	}
}
