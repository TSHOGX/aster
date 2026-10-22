package authoring

import (
	"context"
	"volume-studio/backend/internal/history"
)

func (s *Store) pinConflictInputs(ctx context.Context, library, id, base, main, source string) error {
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	coordinator, err := history.Coordinate(ctx, repo)
	if err != nil {
		return err
	}
	defer coordinator.Close()
	for _, pin := range []struct{ name, commit string }{{"base", base}, {"main", main}, {"source", source}} {
		operation, err := coordinator.Update(ctx, "conflict-"+id+"-"+pin.name, "refs/conflicts/"+id+"/"+pin.name, "", pin.commit)
		if err != nil {
			return err
		}
		if operation.State != "settled" {
			return ErrConflict
		}
	}
	return nil
}

// Reestablish durable workspace pins before serving requests after restart.
func (s *Store) reconcileConflictPins(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT library_id,id,base_commit,main_commit,source_commit FROM conflict_workspaces ORDER BY id")
	if err != nil {
		return err
	}
	type item struct{ library, id, base, main, source string }
	all := []item{}
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.library, &value.id, &value.base, &value.main, &value.source); err != nil {
			rows.Close()
			return err
		}
		all = append(all, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, value := range all {
		if err = s.pinConflictInputs(ctx, value.library, value.id, value.base, value.main, value.source); err != nil {
			return err
		}
	}
	return nil
}
