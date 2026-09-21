package producthttp

import (
	"net/http"
	"strings"

	"harness-platform/orchestrator/internal/product"
)

// Server exposes the Project product model to browsers. It holds no execution
// state: every request resolves an actor, calls one application method, and
// returns a projection.
type Server struct {
	Store     *Store
	Terminals *product.Terminals
	Agents    *product.Agents
	Files     *product.Files
	Shares    *product.Shares
	Lifecycle *product.Lifecycle
	Auth      Authenticator
	Config    Config
	Profile   product.Profile
}

// Store is the product store surface this transport needs. Declaring it narrowly
// keeps the handler from reaching worker-only operations such as settling a
// child or recording a process exit, which take no actor scope by design.
type Store = product.Store

// Handler routes the product API. Every path under /api/projects resolves the
// Project from the URL and the actor from the request; a child ID alone never
// grants access, because the store authorizes the stored parent in the same
// transaction as the change.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/projects/{project}/schedules", s.listSchedules)
	mux.HandleFunc("POST /api/projects/{project}/schedules", s.createSchedule)
	mux.HandleFunc("GET /api/projects/{project}/schedules/{schedule}", s.getSchedule)
	mux.HandleFunc("PATCH /api/projects/{project}/schedules/{schedule}", s.updateSchedule)
	mux.HandleFunc("DELETE /api/projects/{project}/schedules/{schedule}", s.deleteSchedule)
	mux.HandleFunc("POST /api/projects/{project}/schedules/{schedule}/run", s.runSchedule)
	mux.HandleFunc("GET /api/projects/{project}/schedules/{schedule}/runs", s.scheduleRuns)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/notifications", s.notifications)
	mux.HandleFunc("POST /api/notifications/{notification}/read", s.readNotification)
	mux.HandleFunc("GET /api/conversations", s.sharedConversations)
	mux.HandleFunc("GET /api/conversations/{session}/messages", s.conversationMessages)
	mux.HandleFunc("GET /api/conversations/{session}/events", s.conversationEvents)
	mux.HandleFunc("POST /api/projects/{project}/sessions/{session}/readers", s.grantConversation)
	mux.HandleFunc("DELETE /api/projects/{project}/sessions/{session}/readers/{reader}", s.revokeConversation)
	mux.HandleFunc("POST /api/projects/{project}/sessions/{session}/shares", s.createShare)
	mux.HandleFunc("GET /api/projects/{project}/sessions/{session}/shares", s.listShares)
	mux.HandleFunc("DELETE /api/projects/{project}/shares/{share}", s.revokeShare)
	mux.HandleFunc("POST /api/projects/{project}/shares/{share}/rotate", s.rotateShare)
	mux.HandleFunc("GET /api/projects/{project}/files", s.listFiles)
	mux.HandleFunc("GET /api/projects/{project}/files/content", s.readFile)
	mux.HandleFunc("PUT /api/projects/{project}/files/content", s.writeFile)
	mux.HandleFunc("POST /api/projects/{project}/files", s.mutateFile)
	mux.HandleFunc("GET /api/projects/{project}/files/operations/{operation}", s.fileOperation)
	mux.HandleFunc("GET /api/projects/{project}/files/watch", s.watchFiles)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{project}", s.getProject)
	mux.HandleFunc("PATCH /api/projects/{project}", s.renameProject)
	mux.HandleFunc("DELETE /api/projects/{project}", s.deleteProject)
	mux.HandleFunc("GET /api/projects/{project}/blockers", s.blockers)
	mux.HandleFunc("POST /api/projects/{project}/pause", s.pauseProject)
	mux.HandleFunc("POST /api/projects/{project}/restart", s.restartProject)
	mux.HandleFunc("GET /api/projects/{project}/events", s.events)

	mux.HandleFunc("GET /api/projects/{project}/sessions", s.listSessions)
	mux.HandleFunc("POST /api/projects/{project}/sessions", s.createSession)
	mux.HandleFunc("GET /api/projects/{project}/sessions/{session}", s.getSession)
	mux.HandleFunc("PATCH /api/projects/{project}/sessions/{session}", s.renameSession)
	mux.HandleFunc("DELETE /api/projects/{project}/sessions/{session}", s.deleteSession)
	mux.HandleFunc("GET /api/projects/{project}/sessions/{session}/turns", s.listTurns)
	mux.HandleFunc("POST /api/projects/{project}/sessions/{session}/turns", s.admitTurn)
	mux.HandleFunc("GET /api/projects/{project}/sessions/{session}/messages", s.listMessages)

	mux.HandleFunc("GET /api/projects/{project}/terminals", s.listTerminals)
	mux.HandleFunc("POST /api/projects/{project}/terminals", s.createTerminal)
	mux.HandleFunc("GET /api/projects/{project}/terminals/{terminal}", s.getTerminal)
	mux.HandleFunc("PATCH /api/projects/{project}/terminals/{terminal}", s.renameTerminal)
	mux.HandleFunc("DELETE /api/projects/{project}/terminals/{terminal}", s.deleteTerminal)
	mux.HandleFunc("POST /api/projects/{project}/terminals/{terminal}/start", s.startTerminal)
	mux.HandleFunc("POST /api/projects/{project}/terminals/{terminal}/stop", s.stopTerminal)
	mux.HandleFunc("GET /api/projects/{project}/terminals/{terminal}/attach", s.attachTerminal)

	public := http.NewServeMux()
	public.HandleFunc("GET /api/shared/{token}", s.publicShare)
	public.HandleFunc("GET /api/shared/{token}/messages", s.publicMessages)
	public.HandleFunc("GET /api/shared/{token}/files", s.publicFile)
	public.Handle("/", s.guard(mux))
	return public
}

// guard applies transport policy before any handler runs: origin for state
// changing requests and for the WebSocket handshake, then authentication. Both
// must be settled before a store call, so an unauthenticated probe cannot learn
// whether a resource exists.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		stateChanging := r.Method != http.MethodGet && r.Method != http.MethodHead
		if (stateChanging || upgrade) && !s.Config.originAllowed(r) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		actor, err := s.Auth.Actor(r)
		if err != nil || actor == "" {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		next.ServeHTTP(w, r.WithContext(withActor(r.Context(), actor)))
	})
}

func (s *Server) scope(r *http.Request) product.Scope {
	return product.Scope{Actor: actorFrom(r.Context()), ProjectID: r.PathValue("project")}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projects, err := s.Store.Projects(r.Context(), actorFrom(r.Context()), q.Get("after"), intParam(q, "limit", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID, Title string }
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	p, err := s.Store.CreateProject(r.Context(), actorFrom(r.Context()), body.ID, body.Title)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Project(r.Context(), s.scope(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) renameProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title    string
		Revision int64
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if err := s.Store.RenameProject(r.Context(), s.scope(r), body.Revision, body.Title); err != nil {
		fail(w, err)
		return
	}
	s.getProject(w, r)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	revision := int64Param(r.URL.Query(), "revision")
	if err := s.Store.DeleteProject(r.Context(), s.scope(r), revision); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deleting"})
}

func (s *Server) blockers(w http.ResponseWriter, r *http.Request) {
	bs, err := s.Store.Blockers(r.Context(), s.scope(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blockers": bs})
}

func (s *Server) pauseProject(w http.ResponseWriter, r *http.Request) {
	revision := int64Param(r.URL.Query(), "revision")
	if err := s.Lifecycle.Pause(r.Context(), s.scope(r), revision); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pausing"})
}

func (s *Server) restartProject(w http.ResponseWriter, r *http.Request) {
	revision := int64Param(r.URL.Query(), "revision")
	if err := s.Lifecycle.Restart(r.Context(), s.scope(r), revision); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sessions, err := s.Store.Sessions(r.Context(), s.scope(r), q.Get("after"), intParam(q, "limit", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID, Title string }
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	child, err := s.Store.CreateSession(r.Context(), s.scope(r), body.ID, body.Title)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, child)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	child, err := s.Store.Session(r.Context(), s.scope(r), r.PathValue("session"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, child)
}

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title    string
		Revision int64
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if err := s.Store.RenameSession(r.Context(), s.scope(r), r.PathValue("session"), body.Revision, body.Title); err != nil {
		fail(w, err)
		return
	}
	s.getSession(w, r)
}

// deleteSession records cleanup intent. It does not end the runner: a worker
// settles the child once its process supplies termination evidence.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	revision := int64Param(r.URL.Query(), "revision")
	scope := s.scope(r)
	session := r.PathValue("session")
	if err := s.Store.DeleteSession(r.Context(), scope, session, revision); err != nil {
		fail(w, err)
		return
	}
	if s.Agents != nil {
		// Requesting termination is best effort here; the durable intent above is
		// what guarantees cleanup eventually happens.
		_ = s.Agents.Stop(r.Context(), scope.ProjectID, session)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deleting"})
}

func (s *Server) listTurns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	turns, err := s.Store.Turns(r.Context(), s.scope(r), r.PathValue("session"), int64Param(q, "after"), intParam(q, "limit", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"turns": turns})
}

func (s *Server) admitTurn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID string `json:"request_id"`
		Content   string `json:"content"`
		Held      bool   `json:"held"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	turn, err := s.Store.AdmitTurn(r.Context(), s.scope(r), r.PathValue("session"), body.RequestID, body.Content, body.Held)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, turn)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	messages, err := s.Store.Messages(r.Context(), s.scope(r), r.PathValue("session"), int64Param(q, "after"), intParam(q, "limit", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *Server) listTerminals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	terminals, err := s.Store.Terminals(r.Context(), s.scope(r), q.Get("after"), intParam(q, "limit", 0))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminals": terminals})
}

func (s *Server) createTerminal(w http.ResponseWriter, r *http.Request) {
	body := struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Rows  int    `json:"rows"`
		Cols  int    `json:"cols"`
	}{Rows: 24, Cols: 80}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	child, err := s.Store.CreateTerminal(r.Context(), s.scope(r), body.ID, body.Title, body.Rows, body.Cols)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, child)
}

func (s *Server) getTerminal(w http.ResponseWriter, r *http.Request) {
	child, err := s.Store.Terminal(r.Context(), s.scope(r), r.PathValue("terminal"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, child)
}

func (s *Server) renameTerminal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title    string
		Revision int64
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if err := s.Store.RenameTerminal(r.Context(), s.scope(r), r.PathValue("terminal"), body.Revision, body.Title); err != nil {
		fail(w, err)
		return
	}
	s.getTerminal(w, r)
}

// deleteTerminal records cleanup intent and requests termination. Closing a view
// is a different action entirely and reaches no handler at all.
func (s *Server) deleteTerminal(w http.ResponseWriter, r *http.Request) {
	revision := int64Param(r.URL.Query(), "revision")
	scope := s.scope(r)
	terminal := r.PathValue("terminal")
	if err := s.Store.DeleteTerminal(r.Context(), scope, terminal, revision); err != nil {
		fail(w, err)
		return
	}
	if s.Terminals != nil {
		_ = s.Terminals.Stop(r.Context(), scope.ProjectID, terminal)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deleting"})
}

func (s *Server) startTerminal(w http.ResponseWriter, r *http.Request) {
	child, err := s.Store.Terminal(r.Context(), s.scope(r), r.PathValue("terminal"))
	if err != nil {
		fail(w, err)
		return
	}
	body := struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}{Rows: child.Rows, Cols: child.Cols}
	if r.ContentLength > 0 && !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	i, err := s.Terminals.Start(r.Context(), s.scope(r), child.ID, body.Rows, body.Cols)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, terminalProcess(i))
}

// stopTerminal requests termination. It reports accepted rather than ok: only the
// process's exit frame establishes that it ended, and with what code.
func (s *Server) stopTerminal(w http.ResponseWriter, r *http.Request) {
	scope := s.scope(r)
	if _, err := s.Store.Terminal(r.Context(), scope, r.PathValue("terminal")); err != nil {
		fail(w, err)
		return
	}
	if err := s.Terminals.Stop(r.Context(), scope.ProjectID, r.PathValue("terminal")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

// terminalProcess is the projection of a PTY incarnation. The engine handle is
// deliberately absent: it is a stored binding, never something a browser holds
// or sends back as authority.
func terminalProcess(i product.TerminalIncarnation) map[string]any {
	return map[string]any{
		"incarnation": i.ID,
		"terminal_id": i.TerminalID,
		"state":       i.State,
		"rows":        i.Rows,
		"cols":        i.Cols,
		"exit_code":   i.ExitCode,
		"error":       i.Error,
	}
}
