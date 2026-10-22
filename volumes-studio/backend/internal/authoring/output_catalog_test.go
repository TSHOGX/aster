package authoring

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

func TestEvolutionOutputCatalogFiltersBeforeCountsAndExposesReadOnlyMetadata(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	query := evolution.OutputCatalogQuery{GroupID: "group", Limit: 25}
	page, err := s.EvolutionOutputCatalog(ctx, testActor, query)
	if err != nil || page.Total < 1 {
		t.Fatal(page, err)
	}
	_, err = s.ReserveEvolutionPrefix(ctx, testActor, PrefixReservation{LibraryID: l.ID, Prefix: "skills", GroupID: "group", BindingID: "binding", Generation: 1, OperationID: "reservation"}, "reservation")
	if err != nil {
		t.Fatal(err)
	}
	query.LibraryID = l.ID
	detail, err := s.EvolutionOutputLibrary(ctx, testActor, query)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Library.OwnerGroup != "group" || !detail.Library.CanConfigure || detail.Main.Validate() != nil || len(detail.Reservations) != 1 || detail.Reservations[0].BindingID != "binding" {
		t.Fatal(detail)
	}
	query.GroupID = "foreign"
	detail, err = s.EvolutionOutputLibrary(ctx, testActor, query)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Reservations[0].BindingID != "" || detail.Library.OwnerGroup != "group" {
		t.Fatal("foreign binding identity leaked", detail)
	}
	s.config.Authority = &branchAuthority{false}
	query.LibraryID = ""
	page, err = s.EvolutionOutputCatalog(ctx, testActor, query)
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("denied library leaked through count", page, err)
	}
	query.LibraryID = l.ID
	if _, err = s.EvolutionOutputLibrary(ctx, testActor, query); err == nil {
		t.Fatal("denied detail returned")
	}
}
