package product

import "testing"

func TestEventRetentionUsesActualDeletedIDsAcrossProjects(t *testing.T) {
	s, a, b := fixture(t)
	_, baseline, err := s.EventBounds(ctx, a)
	must(t, err)
	for n := 0; n < 10; n++ {
		p, e := s.Project(ctx, b)
		must(t, e)
		must(t, s.RenameProject(ctx, b, p.Revision, "changed"))
	}
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, s.RenameProject(ctx, a, p.Revision, "new event"))
	frontier, err := s.EventRetentionFrontier(ctx, a)
	must(t, err)
	if frontier != 0 {
		t.Fatal("cross-project gaps manufactured retention loss", frontier)
	}
	_, err = s.db.Exec(`DELETE FROM events WHERE project_id=? AND id<=?`, a.ProjectID, baseline)
	must(t, err)
	frontier, err = s.EventRetentionFrontier(ctx, a)
	must(t, err)
	if frontier != baseline {
		t.Fatal(frontier, baseline)
	}
}
