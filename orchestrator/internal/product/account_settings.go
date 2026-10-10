package product

import (
	"context"
	"database/sql"
	"errors"
)

type AccountPreferences struct {
	Revision  int64  `json:"revision"`
	ThemeMode string `json:"theme_mode"`
	Accent    string `json:"accent"`
}
type PreferenceChanges struct {
	ThemeMode *string `json:"theme_mode,omitempty"`
	Accent    *string `json:"accent,omitempty"`
}
type SelfProfile struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Revision    int64  `json:"revision"`
}

func profileOf(a Account) SelfProfile {
	return SelfProfile{a.ID, a.Username, a.DisplayName, a.Revision}
}
func preferences(ctx context.Context, q queryer, account string) (AccountPreferences, error) {
	p := AccountPreferences{ThemeMode: "system", Accent: "neutral"}
	err := q.QueryRowContext(ctx, `SELECT revision,theme_mode,accent FROM account_preferences WHERE account_id=?`, account).Scan(&p.Revision, &p.ThemeMode, &p.Accent)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}
func (s *Store) normalLogin(ctx context.Context, q queryer, token string) (LoginSession, error) {
	session, err := s.readLogin(ctx, q, token)
	if err != nil {
		return session, err
	}
	if session.Restricted {
		return LoginSession{}, ErrUnauthorized
	}
	return session, nil
}
func (s *Store) AccountPreferences(ctx context.Context, token string) (AccountPreferences, error) {
	session, err := s.normalLogin(ctx, s.reader, token)
	if err != nil {
		return AccountPreferences{}, err
	}
	return preferences(ctx, s.reader, session.Account.ID)
}
func (s *Store) UpdateAccountPreferences(ctx context.Context, token string, expected int64, changes PreferenceChanges) (AccountPreferences, error) {
	if expected < 0 || changes.ThemeMode == nil && changes.Accent == nil {
		return AccountPreferences{}, Admission("preferences_invalid", ErrInvalid)
	}
	if changes.ThemeMode != nil && *changes.ThemeMode != "system" && *changes.ThemeMode != "light" && *changes.ThemeMode != "dark" {
		return AccountPreferences{}, Admission("theme_mode_invalid", ErrInvalid)
	}
	if changes.Accent != nil && *changes.Accent != "neutral" && *changes.Accent != "blue" && *changes.Accent != "green" && *changes.Accent != "violet" {
		return AccountPreferences{}, Admission("accent_invalid", ErrInvalid)
	}
	var result AccountPreferences
	err := s.write(ctx, func(tx *sql.Tx) error {
		session, err := s.normalLogin(ctx, tx, token)
		if err != nil {
			return err
		}
		result, err = preferences(ctx, tx, session.Account.ID)
		if err != nil {
			return err
		}
		if result.Revision != expected {
			return Admission("revision_conflict", ErrConflict)
		}
		if changes.ThemeMode != nil {
			result.ThemeMode = *changes.ThemeMode
		}
		if changes.Accent != nil {
			result.Accent = *changes.Accent
		}
		result.Revision++
		_, err = tx.ExecContext(ctx, `INSERT INTO account_preferences(account_id,revision,theme_mode,accent) VALUES(?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET revision=excluded.revision,theme_mode=excluded.theme_mode,accent=excluded.accent`, session.Account.ID, result.Revision, result.ThemeMode, result.Accent)
		return err
	})
	return result, err
}
func (s *Store) SelfProfile(ctx context.Context, token string) (SelfProfile, error) {
	session, err := s.normalLogin(ctx, s.reader, token)
	if err != nil {
		return SelfProfile{}, err
	}
	return profileOf(session.Account), nil
}
func (s *Store) UpdateSelfProfile(ctx context.Context, token string, expected int64, displayName string) (SelfProfile, error) {
	if expected < 1 || !validateDisplayName(displayName) {
		return SelfProfile{}, Admission("display_name_invalid", ErrInvalid)
	}
	var result SelfProfile
	err := s.write(ctx, func(tx *sql.Tx) error {
		session, err := s.normalLogin(ctx, tx, token)
		if err != nil {
			return err
		}
		a := session.Account
		if a.Revision != expected {
			return Admission("revision_conflict", ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET display_name=?,revision=revision+1,updated_at=? WHERE id=?`, displayName, s.timestamp(), a.ID); err != nil {
			return err
		}
		a.DisplayName = displayName
		a.Revision++
		result = profileOf(a)
		return s.auditAccount(ctx, tx, a.ID, "account.profile.update", a.ID, a.Revision)
	})
	return result, err
}
