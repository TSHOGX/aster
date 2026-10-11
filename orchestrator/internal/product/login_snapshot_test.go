package product

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// Cancellation must not strand a SQLite statement and pin a pooled WAL snapshot:
// both immediate login visibility and committed revocation depend on fresh reads.
func TestCanceledReadersCannotRetainLoginAuthority(t *testing.T) {
	s, account := accountStore(t)
	s.reader.SetMaxOpenConns(1)
	s.reader.SetMaxIdleConns(1)
	token := adminTestToken(t, s, account)
	query := `SELECT ` + strings.TrimSuffix(strings.Repeat("token_digest,", 256), ",") + ` FROM auth_sessions WHERE account_id=?`
	cancellations := 0
	iterations := 5000
	if testing.Short() {
		iterations = 50
	}
	for iteration := 0; iteration < iterations; iteration++ {
		readCtx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(iteration%300+1)*time.Microsecond, cancel)
		rows, err := s.reader.QueryContext(readCtx, query, account.ID)
		if err == nil {
			rows.Close()
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		} else {
			cancellations++
		}
		timer.Stop()
		cancel()
		replacement, err := newShareToken()
		must(t, err)
		must(t, s.write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM auth_sessions WHERE account_id=?`, account.ID); err != nil {
				return err
			}
			return s.insertLogin(context.Background(), tx, replacement, account, false, time.Hour)
		}))
		if _, err = s.Login(context.Background(), token); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("iteration %d: committed revocation not visible: %v", iteration, err)
		}
		if _, err = s.Login(context.Background(), replacement); err != nil {
			t.Fatalf("iteration %d: committed login not visible: %v", iteration, err)
		}
		token = replacement
	}
	if cancellations == 0 {
		t.Fatal("test did not exercise reader cancellation")
	}
}
