package producthttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"harness-platform/orchestrator/internal/product"
)

// Terminal socket bounds. A browser connection must never be able to consume
// unbounded application memory or hold a write slot open indefinitely.
const (
	socketReadLimit   = 1 << 20
	socketWriteWait   = authorityWriteWait
	socketPingPeriod  = 30 * time.Second
	socketPongWait    = 70 * time.Second
	socketReauthorize = time.Second
)

// clientMessage is the text-frame control protocol. Raw input arrives as binary
// frames instead, so keystrokes never pass through JSON escaping.
type clientMessage struct {
	Type   string `json:"type"`
	Rows   int    `json:"rows,omitempty"`
	Cols   int    `json:"cols,omitempty"`
	Signal int32  `json:"signal,omitempty"`
}

// attachTerminal upgrades one browser view onto a live PTY. The upgrade carries
// no authority of its own: origin and identity were settled by guard, and the
// attach call authorizes the Project parent in the store.
//
// Closing this socket detaches one observer. It never stops the PTY, and it never
// stops the Project: that is the whole point of a retained terminal.
func (s *Server) attachTerminal(w http.ResponseWriter, r *http.Request) {
	scope := s.scope(r)
	terminal := r.PathValue("terminal")
	wantInput := r.URL.Query().Get("input") == "1"

	// Reject before upgrading when the terminal is unknown or not running, so the
	// client sees an HTTP status rather than a socket that opens and dies.
	observer, err := s.Terminals.Attach(r.Context(), scope, terminal, wantInput)
	if err != nil {
		fail(w, err)
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 32 << 10,
		// Origin was already enforced in guard against the forwarded host, which is
		// the only place that knows this deployment's public origin.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote its own status.
		observer.Close()
		return
	}
	defer conn.Close()
	defer observer.Close()

	conn.SetReadLimit(socketReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(socketPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(socketPongWait))
	})

	done := make(chan struct{})
	// The reader goroutine owns all client input. The writer loop below owns every
	// write to this connection, including pings, because a gorilla connection
	// supports exactly one concurrent writer.
	go func() {
		defer close(done)
		s.readSocket(r, conn, observer, scope, terminal)
	}()
	s.writeSocket(r, conn, observer, scope, done)
}

func (s *Server) readSocket(r *http.Request, conn *websocket.Conn, observer *product.TerminalObserver, scope product.Scope, terminal string) {
	allowance, controls := float64(1<<20), float64(100)
	last := time.Now()

	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		actor, e := s.Auth.Actor(r)
		if e != nil || actor != scope.Actor {
			return
		}
		elapsed := time.Since(last).Seconds()
		last = time.Now()
		allowance = min(float64(1<<20), allowance+elapsed*(64<<10))
		controls = min(float64(100), controls+elapsed*100)
		allowance -= float64(len(data))
		controls--
		if allowance < 0 || controls < 0 {
			return
		}
		switch kind {
		case websocket.BinaryMessage:
			// Raw keystrokes. A read-only observer's input is refused rather than
			// silently dropped, so a user is never left typing into nothing.
			if err := s.Terminals.Input(r.Context(), observer, data); err != nil {
				if errors.Is(err, product.ErrConflict) {
					continue
				}
				return
			}
		case websocket.TextMessage:
			var message clientMessage
			if err := json.Unmarshal(data, &message); err != nil {
				continue
			}
			switch message.Type {
			case "resize":
				_ = s.Terminals.ObserverResize(r.Context(), scope, observer, message.Rows, message.Cols)
			case "claim_input":
				_ = s.Terminals.ClaimInput(observer)
			case "eof":
				_ = s.Terminals.ObserverEOF(r.Context(), observer)
			case "interrupt":
				// Ctrl-C is a PTY byte, not a directed signal: the line discipline
				// delivers SIGINT to the foreground process group, leaving the shell
				// itself running. Signalling the handle would kill the shell instead.
				_ = s.Terminals.Interrupt(r.Context(), observer)
			case "signal":
				// Only termination is expressible, and only from the input holder. A
				// browser cannot ask the application to deliver an arbitrary signal,
				// and a read-only view cannot end someone else's process.
				if observer.HoldsInput() && (message.Signal == 15 || message.Signal == 9) {
					_ = s.Terminals.ObserverSignal(r.Context(), observer, message.Signal)
				}
			}
		}
	}
}

func (s *Server) writeSocket(r *http.Request, conn *websocket.Conn, observer *product.TerminalObserver, scope product.Scope, done <-chan struct{}) {
	ping := time.NewTicker(socketPingPeriod)
	defer ping.Stop()
	// Authorization is rechecked for the life of the connection: revoking access
	// must close a live subscription, not merely refuse the next new one.
	recheck := time.NewTicker(socketReauthorize)
	defer recheck.Stop()
	invalidations := identityInvalidations(s.Auth)
	authorized := func() bool {
		actor, e := s.Auth.Actor(r)
		if e != nil || actor != scope.Actor {
			s.closeSocket(conn, websocket.ClosePolicyViolation, "login no longer valid")
			return false
		}
		if _, err := s.Store.Project(r.Context(), scope); err != nil {
			s.closeSocket(conn, websocket.ClosePolicyViolation, closeReason(err))
			return false
		}
		return true
	}
	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(socketWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-invalidations:
			invalidations = identityInvalidations(s.Auth)
			if !authorized() {
				return
			}
		case <-recheck.C:
			if !authorized() {
				return
			}

		case frame, ok := <-observer.Frames():
			if !ok {
				// The attachment ended: the process exited, the placement was lost, or
				// this observer was disconnected. The close frame says which.
				s.closeSocket(conn, websocket.CloseNormalClosure, "attachment ended")
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(socketWriteWait))
			if frame.Kind == "data" {
				if err := conn.WriteMessage(websocket.BinaryMessage, frame.Data); err != nil {
					return
				}
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, encode(frame)); err != nil {
				return
			}
			if frame.Kind == "exit" || frame.Kind == "lost" {
				s.closeSocket(conn, websocket.CloseNormalClosure, frame.Kind)
				return
			}
		}
	}
}

func (s *Server) closeSocket(conn *websocket.Conn, code int, reason string) {
	_ = conn.SetWriteDeadline(time.Now().Add(socketWriteWait))
	_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
}
