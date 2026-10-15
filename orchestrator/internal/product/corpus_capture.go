package product

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"aster/contracts/evolution"
)

const corpusEpisodeLimit = 64 << 10
const corpusSnapshotLimit = 4 << 20

type CorpusCaptureInput struct {
	RequestID      string `json:"request_id"`
	SourceID       string `json:"source_id"`
	SourceRevision int64  `json:"source_revision"`
	ExecutionID    string `json:"execution_id"`
}
type CorpusEpisodeRevision struct {
	Episode      evolution.CorpusEpisode `json:"episode"`
	Revision     int64                   `json:"revision"`
	Digest       string                  `json:"digest"`
	Sequence     int64                   `json:"sequence"`
	CaptureState string                  `json:"capture_state"`
}

func sourcePolicy(ctx context.Context, q queryer, group, source string, revision int64) (evolution.SourceBinding, error) {
	var result evolution.SourceBinding
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM evolution_source_policies WHERE group_id=? AND source_id=? AND revision=?", group, source, revision).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrForbidden
		}
		return result, err
	}
	result, err := evolution.Decode[evolution.SourceBinding](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	audience := false
	for _, id := range result.AudienceGroups {
		if id == group {
			audience = true
		}
	}
	if !audience || !result.DerivativePublication || len(result.Exclusions) > 0 {
		return result, ErrForbidden
	}
	if err = authorizeEvolutionSources(ctx, q, evolution.Configuration{GroupID: group, Sources: []evolution.SourceBinding{result}}); err != nil {
		return result, err
	}
	return result, nil
}

// CaptureCorpusEpisode is explicit authorized reuse, not administrative stored-
// message inspection. Late capture/outcome data creates a new immutable revision.
func (s *Store) CaptureCorpusEpisode(ctx context.Context, token, group string, input CorpusCaptureInput) (CorpusEpisodeRevision, error) {
	var result CorpusEpisodeRevision
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.manage")
		if err != nil {
			return err
		}
		result, err = s.captureCorpusEpisode(ctx, tx, actor, group, input)
		return err
	})
	if err != nil {
		return CorpusEpisodeRevision{}, err
	}
	return result, nil
}

func (s *Store) captureCorpusEpisode(ctx context.Context, tx *sql.Tx, actor Account, group string, input CorpusCaptureInput) (CorpusEpisodeRevision, error) {
	var result CorpusEpisodeRevision
	if !validID(group) || !validID(input.SourceID) || !validID(input.ExecutionID) || input.SourceRevision < 1 {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "corpus.capture", group, input)
	if err != nil {
		return result, err
	}
	err = func() error {
		if err := authorizeEvolutionGroup(ctx, tx, actor, group, "config.manage"); err != nil {
			return err
		}
		if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
			return err
		}
		source, err := sourcePolicy(ctx, tx, group, input.SourceID, input.SourceRevision)
		if err != nil {
			return err
		}
		prior, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			return json.Unmarshal(prior, &result)
		}
		var project, session, prompt, state, outcome string
		err = tx.QueryRowContext(ctx, `SELECT e.project_id,e.session_id,e.prompt_id,e.state,e.outcome FROM execution_runs e JOIN sessions s ON s.id=e.session_id JOIN projects p ON p.id=e.project_id WHERE e.id=? AND s.deletion_intent=0 AND p.state NOT IN('deleting','deleted') AND ((?='project' AND e.project_id=?) OR (?='session' AND e.session_id=?))`, input.ExecutionID, source.Kind, source.ResourceID, source.Kind, source.ResourceID).Scan(&project, &session, &prompt, &state, &outcome)
		if err != nil {
			return missing(err)
		}
		if source.Kind == "project" && source.ResourceID != project || source.Kind == "session" && source.ResourceID != session || source.Kind != "project" && source.Kind != "session" {
			return ErrForbidden
		}
		if state != "settled" && state != "failed" && state != "unknown" {
			return ErrConflict
		}
		if outcome != "completed" && outcome != "failed" {
			outcome = "unknown"
		}
		episode := evolution.CorpusEpisode{ID: input.ExecutionID, FamilyID: prompt, Outcome: outcome, Text: "", Truncated: false}
		rows, err := tx.QueryContext(ctx, "SELECT role,substr(content,1,65536),length(CAST(content AS BLOB)) FROM messages WHERE turn_id=? ORDER BY sequence LIMIT 201", prompt)
		if err != nil {
			return err
		}
		var text strings.Builder
		count := 0
		appendText := func(value string) {
			remaining := corpusEpisodeLimit - text.Len()
			if len(value) > remaining {
				value = value[:remaining]
				for !utf8.ValidString(value) && len(value) > 0 {
					value = value[:len(value)-1]
				}
				episode.Truncated = true
			}
			text.WriteString(value)
		}
		appendText("Evidence family is the local prompt ID; imported-copy family provenance is unknown.\n")
		for rows.Next() {
			var role, content string
			var size int
			if err = rows.Scan(&role, &content, &size); err != nil {
				rows.Close()
				return err
			}
			count++
			if count > 200 {
				episode.Truncated = true
				break
			}
			if size > len(content) {
				episode.Truncated = true
			}
			appendText(role + ": " + content + "\n")
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		result.CaptureState = "missing"
		var capturePath, digest string
		var size int64
		rows, err = tx.QueryContext(ctx, "SELECT host_path,digest,size,path FROM native_captures WHERE turn_id=? AND state IN('exported','archived') ORDER BY path", prompt)
		if err != nil {
			return err
		}
		for rows.Next() {
			var host, hash, nativePath string
			var n int64
			if err = rows.Scan(&host, &hash, &n, &nativePath); err != nil {
				rows.Close()
				return err
			}
			if filepath.Base(nativePath) == input.ExecutionID+".jsonl" {
				capturePath, digest, size = host, hash, n
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if capturePath != "" {
			if !filepath.IsAbs(capturePath) || size < 0 || size > 64<<20 {
				return ErrInvalid
			}
			file, err := os.OpenFile(capturePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				file.Close()
				return ErrInvalid
			}
			raw, readErr := io.ReadAll(io.LimitReader(file, size+1))
			file.Close()
			if readErr != nil {
				return readErr
			}
			if int64(len(raw)) != size || evolution.Digest(raw) != digest {
				return ErrConflict
			}
			observations, incomplete, err := observableNativeCapture(raw)
			if err != nil {
				return err
			}
			result.CaptureState = "complete"
			if incomplete {
				result.CaptureState = "partial"
				episode.Truncated = true
			}
			appendText("Native capture is a session snapshot; repeated history is not independent supporting evidence.\n")
			appendText(observations)
		} else {
			episode.Truncated = true
			appendText("Native capture unavailable; stored messages are incomplete trajectory evidence.\n")
		}
		episode.Text = text.String()
		result.Episode = episode
		body, _ := json.Marshal(struct {
			Episode        evolution.CorpusEpisode `json:"episode"`
			SourceRevision int64                   `json:"source_revision"`
			CaptureState   string                  `json:"capture_state"`
		}{episode, input.SourceRevision, result.CaptureState})
		result.Digest = evolution.Digest(body)
		err = tx.QueryRowContext(ctx, "SELECT revision,rowid FROM corpus_episodes WHERE group_id=? AND source_id=? AND episode_id=? AND digest=?", group, input.SourceID, input.ExecutionID, result.Digest).Scan(&result.Revision, &result.Sequence)
		if errors.Is(err, sql.ErrNoRows) {
			if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0)+1 FROM corpus_episodes WHERE group_id=? AND source_id=? AND episode_id=?", group, input.SourceID, input.ExecutionID).Scan(&result.Revision); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO corpus_episodes VALUES(?,?,?,?,?,?,?,?)", group, input.SourceID, input.ExecutionID, result.Revision, input.SourceRevision, body, result.Digest, s.timestamp()); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, "SELECT last_insert_rowid()").Scan(&result.Sequence); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		receipt, _ := json.Marshal(result)
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, receipt); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "corpus.capture", "source", input.SourceID, input.RequestID, result.Revision, map[string]any{"group_id": group, "execution_id": input.ExecutionID, "source_revision": input.SourceRevision, "capture_state": result.CaptureState, "digest": result.Digest})
	}()
	if err != nil {
		return CorpusEpisodeRevision{}, err
	}
	return result, nil
}

// Only observable text/tool-call/tool-result blocks are exported. Thinking,
// signatures, provider credentials and unknown event payloads are not copied.
func observableNativeCapture(raw []byte) (string, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var result strings.Builder
	incomplete := false
	messages := 0
	for scanner.Scan() {
		if _, err := evolution.Decode[json.RawMessage](bytes.NewReader(scanner.Bytes())); err != nil {
			return "", false, ErrInvalid
		}
		var row struct {
			Type    string `json:"type"`
			Message struct {
				Role     string          `json:"role"`
				Content  json.RawMessage `json:"content"`
				ToolName string          `json:"toolName"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			return "", false, ErrInvalid
		}
		if row.Type != "message" {
			// Compaction or unknown native entries can omit observable history. Only
			// known non-content metadata is safe to ignore without marking incompleteness.
			if row.Type != "session" && row.Type != "model_change" && row.Type != "thinking_level_change" {
				incomplete = true
			}
			continue
		}
		messages++
		if row.Message.Role != "user" && row.Message.Role != "assistant" && row.Message.Role != "toolResult" {
			incomplete = true
			continue
		}
		var text string
		if json.Unmarshal(row.Message.Content, &text) == nil {
			result.WriteString(row.Message.Role + ": " + text + "\n")
		} else {
			var blocks []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(row.Message.Content, &blocks) != nil {
				incomplete = true
				continue
			}
			for _, block := range blocks {
				switch block.Type {
				case "text":
					result.WriteString(row.Message.Role + ": " + block.Text + "\n")
				case "toolCall":
					result.WriteString("tool call " + block.Name + ": " + string(block.Arguments) + "\n")
				case "thinking":
				default:
					incomplete = true
				}
			}
		}
		if result.Len() > corpusEpisodeLimit {
			incomplete = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, fmt.Errorf("native capture line exceeds bound")
	}
	if messages == 0 {
		incomplete = true
	}
	return result.String(), incomplete, nil
}
