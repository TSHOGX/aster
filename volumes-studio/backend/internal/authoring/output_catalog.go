package authoring

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) outputLibraries(ctx context.Context, a Actor, search string) ([]evolution.OutputLibrary, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,profile,archived,evolution_owner_group FROM libraries ORDER BY id")
	if err != nil {
		return nil, err
	}
	var candidates []evolution.OutputLibrary
	for rows.Next() {
		var item evolution.OutputLibrary
		if err = rows.Scan(&item.ID, &item.Name, &item.Profile, &item.Archived, &item.OwnerGroup); err != nil {
			break
		}
		candidates = append(candidates, item)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	result := []evolution.OutputLibrary{}
	for _, item := range candidates {
		if err = s.authorizeLibrary(ctx, a, item.ID, "volume.read"); errors.Is(err, ErrAuthority) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err = s.authorizeLibrary(ctx, a, item.ID, "volume.configure"); err == nil {
			item.CanConfigure = true
		} else if !errors.Is(err, ErrAuthority) {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(item.Name+"\n"+item.ID), strings.ToLower(search)) {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}
func (s *Store) EvolutionOutputCatalog(ctx context.Context, a Actor, input evolution.OutputCatalogQuery) (evolution.OutputCatalogPage, error) {
	var result evolution.OutputCatalogPage
	if input.Validate() != nil || input.LibraryID != "" {
		return result, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.outputLibraries(ctx, a, input.Search)
	if err != nil {
		return result, err
	}
	page, err := pageOf(items, Query{Q: input.Search, Cursor: input.Cursor, Limit: input.Limit}, requestDigest("evolution-output", input.GroupID, a), 25)
	if err != nil {
		return result, err
	}
	return evolution.OutputCatalogPage{Items: page.Items, Next: page.Next, Total: page.Total}, nil
}
func (s *Store) EvolutionOutputLibrary(ctx context.Context, a Actor, input evolution.OutputCatalogQuery) (evolution.OutputLibraryDetail, error) {
	result := evolution.OutputLibraryDetail{Reservations: []evolution.OutputReservation{}}
	if input.Validate() != nil || input.LibraryID == "" {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, input.LibraryID, "volume.read"); err != nil {
		return result, err
	}
	s.mu.Lock()
	err := func() error {
		if err := s.db.QueryRowContext(ctx, "SELECT id,name,profile,archived,evolution_owner_group FROM libraries WHERE id=?", input.LibraryID).Scan(&result.Library.ID, &result.Library.Name, &result.Library.Profile, &result.Library.Archived, &result.Library.OwnerGroup); err != nil {
			return normalizeDB(err)
		}
		err := s.authorizeLibrary(ctx, a, input.LibraryID, "volume.configure")
		if err == nil {
			result.Library.CanConfigure = true
		} else if !errors.Is(err, ErrAuthority) {
			return err
		}
		rows, err := s.db.QueryContext(ctx, "SELECT prefix,owner_group_id,binding_id,generation,state FROM evolution_prefix_reservations WHERE library_id=? ORDER BY prefix", input.LibraryID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item evolution.OutputReservation
			if err = rows.Scan(&item.Prefix, &item.GroupID, &item.BindingID, &item.Generation, &item.State); err != nil {
				break
			}
			if item.GroupID != input.GroupID {
				item.BindingID = ""
			}
			result.Reservations = append(result.Reservations, item)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return err
		}
		var published evolution.ReleaseRef
		err = s.db.QueryRowContext(ctx, "SELECT (SELECT value FROM metadata WHERE key='publisher_id'),library_id,id,descriptor_digest FROM versions WHERE library_id=? AND state='published' ORDER BY published_at DESC,id DESC LIMIT 1", input.LibraryID).Scan(&published.PublisherID, &published.LibraryID, &published.VersionID, &published.DescriptorDigest)
		if err == nil {
			result.Published = &published
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return nil
	}()
	s.mu.Unlock()
	if err != nil {
		return evolution.OutputLibraryDetail{}, err
	}
	result.Main, err = s.MainCommit(ctx, a, input.LibraryID)
	if err != nil {
		return evolution.OutputLibraryDetail{}, err
	}
	return result, nil
}
