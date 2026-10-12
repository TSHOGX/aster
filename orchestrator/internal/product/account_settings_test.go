package product

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestPreferencesInitializeOnceAndPreserveUnrelatedFields(t *testing.T) {
	ctx := context.Background()
	s, _ := accountStore(t)
	_, token, err := s.Authenticate(ctx, "admin", "permanent-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.AccountPreferences(ctx, token)
	if err != nil || initial != (AccountPreferences{ThemeMode: "system", Accent: "neutral"}) {
		t.Fatal(initial, err)
	}
	dark, violet := "dark", "violet"
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, change := range []PreferenceChanges{{ThemeMode: &dark}, {Accent: &violet}} {
		wg.Add(1)
		go func(change PreferenceChanges) {
			defer wg.Done()
			<-start
			_, err := s.UpdateAccountPreferences(ctx, token, 0, change)
			results <- err
		}(change)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	current, err := s.AccountPreferences(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	change := PreferenceChanges{Accent: &violet}
	if current.ThemeMode != "dark" {
		change = PreferenceChanges{ThemeMode: &dark}
	}
	current, err = s.UpdateAccountPreferences(ctx, token, current.Revision, change)
	if err != nil || current.ThemeMode != "dark" || current.Accent != "violet" || current.Revision != 2 {
		t.Fatal(current, err)
	}
	if _, err = s.UpdateAccountPreferences(ctx, token, 1, PreferenceChanges{Accent: &violet}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestSelfSettingsUseCurrentSessionAndAreIndependentOfMembership(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	account, temporary, err := createTestAccount(t, s, admin.ID, "ordinary", "Ordinary", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, setup, err := s.Authenticate(ctx, "ordinary", temporary)
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := s.ChangePassword(ctx, setup, "", "permanent-user-password", true)
	account = session.Account
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.exec(ctx, `DELETE FROM account_memberships WHERE account_id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	green := "green"
	if _, err = s.UpdateAccountPreferences(ctx, token, 0, PreferenceChanges{Accent: &green}); err != nil {
		t.Fatal("self settings required product membership", err)
	}
	profile, err := s.UpdateSelfProfile(ctx, token, account.Revision, "New display name")
	if err != nil || profile.ID != account.ID || profile.Username != account.Username {
		t.Fatal(profile, err)
	}
	if _, err = s.Login(ctx, token); err != nil {
		t.Fatal("display name change revoked session", err)
	}
	if _, err = s.UpdateSelfProfile(ctx, token, account.Revision, "Stale name"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.RevokeLogin(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateAccountPreferences(ctx, token, 1, PreferenceChanges{Accent: &green}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked session changed settings", err)
	}
	if _, err = s.UpdateSelfProfile(ctx, token, profile.Revision, "Forged name"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
}
