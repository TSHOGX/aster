package product

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
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
func requireAdmin(ctx context.Context, tx *sql.Tx, actor string) error {
	a, err := readAccount(ctx, tx, "id", actor)
	if err != nil || a.Status != "active" || a.Role != "admin" || a.MustChangePassword {
		return ErrForbidden
	}
	return nil
}
func (s *Store) auditAccount(ctx context.Context, tx *sql.Tx, actor, action, target string, revision int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO account_audit(actor_id,action,target_id,revision,created_at) VALUES(?,?,?,?,?)`, actor, action, target, revision, s.timestamp())
	return err
}
func (s *Store) insertAccount(ctx context.Context, tx *sql.Tx, name, display, role, hash string, temporary bool) (Account, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE username=?`, name).Scan(&count); err != nil {
		return Account{}, err
	}
	if count != 0 {
		return Account{}, ErrConflict
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

// CreateAccount returns a temporary password once. No recoverable secret is stored.
func (s *Store) CreateAccount(ctx context.Context, actor, username, display, role string) (Account, string, error) {
	name, err := canonicalUsername(username)
	if err != nil || !validateDisplayName(display) || (role != "admin" && role != "user") {
		return Account{}, "", ErrInvalid
	}
	raw := make([]byte, 18)
	if _, err = rand.Read(raw); err != nil {
		return Account{}, "", err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Account{}, "", err
	}
	var account Account
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := requireAdmin(ctx, tx, actor); err != nil {
			return err
		}
		var err error
		account, err = s.insertAccount(ctx, tx, name, display, role, hash, true)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_memberships(account_id,group_id) VALUES(?,?)`, account.ID, StandardUsersGroup); err != nil {
			return err
		}
		return s.auditAccount(ctx, tx, actor, "account.create", account.ID, account.Revision)
	})
	if err != nil {
		return Account{}, "", err
	}
	return account, password, nil
}

func (s *Store) ResetAccountPassword(ctx context.Context, actor, id string, expected int64) (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	err := s.resetAccount(ctx, actor, id, "", password, expected, false)
	if err != nil {
		return "", err
	}
	return password, nil
}
func (s *Store) RecoverAdmin(ctx context.Context, username, password string) error {
	name, err := canonicalUsername(username)
	if err != nil {
		return err
	}
	return s.resetAccount(ctx, "operator", "", name, password, 0, true)
}
func (s *Store) resetAccount(ctx context.Context, actor, id, name, password string, expected int64, recoverAdmin bool) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if !recoverAdmin {
			if err := requireAdmin(ctx, tx, actor); err != nil {
				return err
			}
		}
		column, value := "id", id
		if recoverAdmin {
			column, value = "username", name
		}
		a, err := readAccount(ctx, tx, column, value)
		if err != nil {
			return err
		}
		if !recoverAdmin && a.Revision != expected {
			return ErrConflict
		}
		role, status := a.Role, a.Status
		if recoverAdmin {
			role, status = "admin", "active"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET password_hash=?,must_change_password=1,temporary_consumed=0,temporary_expires_at=?,role=?,status=?,revision=revision+1,session_revision=session_revision+1,updated_at=? WHERE id=?`, hash, sqlitex.FormatTime(s.now().Add(24*time.Hour)), role, status, s.timestamp(), a.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, a.ID); err != nil {
			return err
		}
		action := "account.password.reset"
		if recoverAdmin {
			action = "account.recover"
		}
		return s.auditAccount(ctx, tx, actor, action, a.ID, a.Revision+1)
	})
}

// ChangeAccountAuthority serializes last-admin protection with the mutation.
func (s *Store) ChangeAccountAuthority(ctx context.Context, actor, id, role, status string, expected int64) (Account, error) {
	if (role != "admin" && role != "user") || (status != "active" && status != "disabled") {
		return Account{}, ErrInvalid
	}
	var result Account
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireAdmin(ctx, tx, actor); err != nil {
			return err
		}
		a, err := readAccount(ctx, tx, "id", id)
		if err != nil {
			return err
		}
		if a.Revision != expected {
			return ErrConflict
		}
		if a.Role == "admin" && a.Status == "active" && (role != "admin" || status != "active") {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE role='admin' AND status='active' AND id<>?`, id).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return Admission("last_admin", ErrConflict)
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET role=?,status=?,revision=revision+1,session_revision=session_revision+1,updated_at=? WHERE id=?`, role, status, s.timestamp(), id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, id); err != nil {
			return err
		}
		if err = s.auditAccount(ctx, tx, actor, "account.authority", id, a.Revision+1); err != nil {
			return err
		}
		next, err := readAccount(ctx, tx, "id", id)
		result = next.Account
		return err
	})
	return result, err
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
