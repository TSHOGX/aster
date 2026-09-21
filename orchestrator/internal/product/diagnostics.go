package product

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Store) ValidateReleasedBindings(ctx context.Context, engine string) error {
	rows, err := s.reader.QueryContext(ctx, `SELECT r.store_id,r.profile FROM project_resources r JOIN projects p ON p.id=r.project_id WHERE p.state<>'deleted'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var store, raw string
		if err = rows.Scan(&store, &raw); err != nil {
			return err
		}
		var profile Profile
		if err = json.Unmarshal([]byte(raw), &profile); err != nil {
			return err
		}
		if store != engine {
			return fmt.Errorf("existing Project belongs to another engine store; original resources must be reconciled before changing engines")
		}
		if profile.ReleaseDigest == "" {
			return fmt.Errorf("existing Project lacks a released runtime identity; use a fresh development database")
		}
	}
	return rows.Err()
}

type DiagnosticCounts struct {
	Projects         map[string]int `json:"projects"`
	Sessions         map[string]int `json:"sessions"`
	Terminals        map[string]int `json:"terminals"`
	Occupied         int            `json:"occupied_projects"`
	PendingLifecycle int            `json:"pending_lifecycle"`
	PendingFiles     int            `json:"pending_files"`
	PendingRecovery  int            `json:"pending_recovery"`
	PendingCapture   int            `json:"pending_capture"`
	DatabaseBytes    int64          `json:"database_bytes"`
}

func (s *Store) OperatorDiagnostics(ctx context.Context) (DiagnosticCounts, error) {
	result := DiagnosticCounts{Projects: map[string]int{}, Sessions: map[string]int{}, Terminals: map[string]int{}}
	for _, item := range []struct {
		table  string
		target map[string]int
	}{{"projects", result.Projects}, {"sessions", result.Sessions}, {"terminals", result.Terminals}} {
		rows, err := s.reader.QueryContext(ctx, `SELECT state,count(*) FROM `+item.table+` GROUP BY state`)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var state string
			var count int
			if err = rows.Scan(&state, &count); err != nil {
				rows.Close()
				return result, err
			}
			item.target[state] = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	err := s.reader.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM project_resources WHERE occupied=1),(SELECT count(*) FROM project_operations WHERE state<>'succeeded'),(SELECT count(*) FROM file_operations WHERE settled=0),(SELECT count(*) FROM agent_incarnations WHERE recovery_state IN ('pending','replaying')),(SELECT count(*) FROM native_captures WHERE state='guest'),(SELECT page_count*page_size FROM pragma_page_count(),pragma_page_size())`).Scan(&result.Occupied, &result.PendingLifecycle, &result.PendingFiles, &result.PendingRecovery, &result.PendingCapture, &result.DatabaseBytes)
	return result, err
}

func (s *Store) OperatorProject(ctx context.Context, id string) (Project, error) {
	return scanProject(s.reader.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, id))
}
