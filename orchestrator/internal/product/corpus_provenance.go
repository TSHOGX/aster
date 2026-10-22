package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"errors"
)

func corpusEvidenceFamily(ctx context.Context, q queryer, turn string) (string, string, error) {
	var family string
	err := q.QueryRowContext(ctx, "SELECT family_id FROM turn_evidence_families WHERE turn_id=?", turn).Scan(&family)
	if err == nil {
		return family, "copy", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	return turn, "local", nil
}
func sourceExcludesEpisode(source evolution.SourceBinding, session, turn, execution string) bool {
	for _, selector := range source.Exclusions {
		if selector == "sessions/"+session || selector == "turns/"+turn || selector == "executions/"+execution {
			return true
		}
	}
	return false
}
