package product

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

func TestGroupCatalogOnlyReturnsBoundDefinitions(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	body := catalogTemplateBody("opaque")
	_, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "bound", Revision: 1, Digest: evolution.Digest(body), Body: body, Enabled: true})
	must(t, err)
	if _, err := s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "bound", 1); err != nil {
		t.Fatal(err)
	}
	values, err := s.GroupCatalog(ctx, token, StandardUsersGroup)
	must(t, err)
	if len(values) != 1 || values[0].CatalogID != "bound" {
		t.Fatal(values)
	}
	if _, err = s.GroupCatalog(ctx, "", StandardUsersGroup); err == nil {
		t.Fatal("unauthenticated group catalog")
	}
}
