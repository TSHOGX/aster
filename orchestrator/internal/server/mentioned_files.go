package server

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"harness-platform/orchestrator/internal/store"
)

var mentionSeparators = regexp.MustCompile("[\\s`\"'*<>|()\\[\\]{}=、，,。;；:：!！?？「」『』【】〈〉《》（）“”‘’~…\\x{3000}\\x{00a0}]+")

func mentionedPaths(content string, paths []string) []string {
	full := map[string]string{}
	names := map[string]string{}
	for _, p := range paths {
		full[strings.ToLower(p)] = p
		parts := strings.Split(p, "/")
		name := strings.ToLower(parts[len(parts)-1])
		if _, ok := names[name]; ok {
			names[name] = ""
		} else {
			names[name] = p
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range mentionSeparators.Split(content, -1) {
		token := strings.TrimRight(strings.TrimSpace(raw), ".·")
		token = strings.TrimPrefix(token, "~/")
		token = strings.TrimPrefix(token, "./")
		token = strings.TrimPrefix(token, "/workspace/")
		token = strings.TrimPrefix(token, "workspace/")
		token = strings.TrimLeft(token, "/")
		if !strings.ContainsAny(token, "/.") {
			continue
		}
		key := strings.ToLower(token)
		hit := full[key]
		if hit == "" {
			hit = names[key]
		}
		if hit != "" && !seen[hit] {
			seen[hit] = true
			out = append(out, hit)
			if len(out) == 12 {
				break
			}
		}
	}
	return out
}
func (s *Server) mentionedFiles(w http.ResponseWriter, r *http.Request, id, mid string) {
	p, ok := s.messageReadScope(w, r, id)
	if ok {
		s.serveMentionedFiles(w, r, p, mid, "")
	}
}
func (s *Server) serveMentionedFiles(w http.ResponseWriter, r *http.Request, p store.MessagePageParams, mid, linkID string) {
	id, e := strconv.ParseInt(mid, 10, 64)
	if e != nil || id <= 0 {
		writeError(w, 400, "invalid message id")
		return
	}
	var content strings.Builder
	offset := 0
	for {
		chunk, e := s.store.MessageContent(r.Context(), p, id, offset, 8192)
		if e != nil {
			readPageError(w, e)
			return
		}
		content.WriteString(chunk.Content)
		if !chunk.HasMore {
			break
		}
		offset = chunk.NextOffset
	}
	files := []store.Artifact{}
	if linkID == "" {
		if !s.refreshRetainedArtifacts(w, r, p.SessionID) {
			return
		}
		files, e = s.store.ListArtifacts(r.Context(), p.SessionID)
	} else {
		var shared []store.ShareLinkFile
		shared, e = s.store.ListShareLinkFiles(r.Context(), linkID)
		for _, f := range shared {
			files = append(files, store.Artifact{Path: f.Path, Size: f.Size})
		}
	}
	if e != nil {
		writeError(w, 500, "cannot read files")
		return
	}
	paths := make([]string, 0, len(files))
	index := map[string]store.Artifact{}
	for _, f := range files {
		paths = append(paths, f.Path)
		index[f.Path] = f
	}
	out := []map[string]any{}
	for _, path := range mentionedPaths(content.String(), paths) {
		f := index[path]
		out = append(out, map[string]any{"path": path, "size": f.Size})
	}
	writeJSON(w, 200, map[string]any{"files": out})
}
