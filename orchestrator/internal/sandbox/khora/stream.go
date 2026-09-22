package khora

import (
	"context"
	"io"
	"sync"

	"connectrpc.com/connect"
)

// Connect's Close drains the response body. It must not run concurrently with
// Receive (or another Close): HTTP response bodies have only one reader, and
// competing drains can strand readers even after the request is cancelled.
// Cancel first to unblock that reader, then serialize the drain and close once.
type receiveStream[T any] struct {
	stream   *connect.ServerStreamForClient[T]
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	closeErr error
}

func (s *receiveStream[T]) receive() (*T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, io.EOF
	}
	if !s.stream.Receive() {
		if err := s.stream.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return s.stream.Msg(), nil
}

func (s *receiveStream[T]) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.closeErr = s.stream.Close()
	}
	return s.closeErr
}
