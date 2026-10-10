package product

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/sqlitex"
)

// Account is the public identity projection. Credentials never leave the store.
type Account struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	Status             string `json:"status"`
	Role               string `json:"role"`
	Revision           int64  `json:"revision"`
	SessionRevision    int64  `json:"session_revision"`
	MustChangePassword bool   `json:"must_change_password"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

type accountRecord struct {
	Account
	hash             string
	temporaryExpires string
	consumed         bool
}

const accountColumns = `id,username,display_name,status,role,revision,session_revision,must_change_password,created_at,updated_at,password_hash,temporary_expires_at,temporary_consumed`

func readAccount(ctx context.Context, q queryer, column, value string) (a accountRecord, err error) {
	// column is selected only by the store's fixed callers, never by a client.
	err = missing(q.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE `+column+`=?`, value).Scan(&a.ID, &a.Username, &a.DisplayName, &a.Status, &a.Role, &a.Revision, &a.SessionRevision, &a.MustChangePassword, &a.CreatedAt, &a.UpdatedAt, &a.hash, &a.temporaryExpires, &a.consumed))
	return
}
func canonicalUsername(name string) (string, error) {
	if len(name) > 64 || !validID(name) {
		return "", ErrInvalid
	}
	return strings.ToLower(name), nil
}
func ValidatePassword(password string) error {
	n := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || n < 12 || n > 128 || len(password) > 512 {
		return Admission("password_invalid", ErrInvalid)
	}
	return nil
}
func validateDisplayName(name string) bool {
	return utf8.ValidString(name) && strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 128 && !strings.ContainsRune(name, 0)
}
func (s *Store) Account(ctx context.Context, id string) (Account, error) {
	a, err := readAccount(ctx, s.reader, "id", id)
	return a.Account, err
}
func (s *Store) auditAccount(ctx context.Context, tx *sql.Tx, actor, action, target string, revision int64) error {
	return s.auditEvent(ctx, tx, actor, action, "account", target, "", revision, nil)
}
func (s *Store) insertAccount(ctx context.Context, tx *sql.Tx, name, display, role, hash string, temporary bool) (Account, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE username=?`, name).Scan(&count); err != nil {
		return Account{}, err
	}
	if count != 0 {
		return Account{}, Admission("username_taken", ErrConflict)
	}
	a := Account{ID: uuid.NewString(), Username: name, DisplayName: display, Status: "active", Role: role, Revision: 1, SessionRevision: 1, MustChangePassword: temporary, CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
	expires := ""
	if temporary {
		expires = sqlitex.FormatTime(s.now().Add(24 * time.Hour))
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO accounts(id,username,display_name,role,password_hash,must_change_password,temporary_expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, name, display, role, hash, temporary, expires, a.CreatedAt, a.UpdatedAt)
	return a, err
}

// BootstrapAccount is an offline operator operation under the Store lifetime lock.
// The first administrator uses the supplied permanent password; recovery and
// browser-created credentials always require the restricted setup exchange.
func (s *Store) BootstrapAccount(ctx context.Context, username, password string, seed BootstrapSeed) (Account, error) {
	if err := seed.Validate(); err != nil {
		return Account{}, err
	}
	name, err := canonicalUsername(username)
	if err != nil {
		return Account{}, err
	}
	if err = ValidatePassword(password); err != nil {
		return Account{}, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Account{}, err
	}
	var account Account
	err = s.write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrConflict
		}
		var err error
		if err = s.insertBootstrapSeed(ctx, tx, seed); err != nil {
			return err
		}
		account, err = s.insertAccount(ctx, tx, name, name, "admin", hash, false)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_memberships(account_id,group_id) VALUES(?,?)`, account.ID, StandardUsersGroup); err != nil {
			return err
		}
		return s.auditAccount(ctx, tx, "operator", "account.bootstrap", account.ID, account.Revision)
	})
	return account, err
}

// RecoverAdmin is confined to the protected operator interface and writer lock.
func (s *Store) RecoverAdmin(ctx context.Context, username, password string) error {
	name, err := canonicalUsername(username)
	if err != nil {
		return err
	}
	if err = ValidatePassword(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return s.writeIdentity(ctx, func(tx *sql.Tx) error {
		before, err := readAccount(ctx, tx, "username", name)
		if err != nil {
			return err
		}
		after, err := s.replaceAccountPassword(ctx, tx, before.Account, hash, "admin", "active")
		if err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, "operator", "account.recover", "account", after.ID, "", after.Revision, map[string]any{"before": accountConfiguration(before.Account), "after": accountConfiguration(after)})
	})
}
func (s *Store) replaceAccountPassword(ctx context.Context, tx *sql.Tx, a Account, hash, role, status string) (Account, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET password_hash=?,must_change_password=1,temporary_consumed=0,temporary_expires_at=?,role=?,status=?,revision=revision+1,session_revision=session_revision+1,updated_at=? WHERE id=?`, hash, sqlitex.FormatTime(s.now().Add(24*time.Hour)), role, status, s.timestamp(), a.ID); err != nil {
		return Account{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, a.ID); err != nil {
		return Account{}, err
	}
	after, err := readAccount(ctx, tx, "id", a.ID)
	return after.Account, err
}

func (s *Store) AccountDirectory(ctx context.Context, actor string) ([]Account, error) {
	a, err := s.Account(ctx, actor)
	if err != nil || a.Status != "active" || a.MustChangePassword {
		return nil, ErrUnauthorized
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,username,display_name FROM accounts WHERE status='active' ORDER BY username LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Username, &a.DisplayName); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
