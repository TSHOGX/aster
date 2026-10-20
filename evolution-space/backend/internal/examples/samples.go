package examples

// Sample shows a newcomer what goes into and comes out of one starter: a small
// corpus, the files the builder writes from it, and what the verifier holds.
type Sample struct {
	Corpus Corpus     `json:"corpus"`
	Files  []File     `json:"files"`
	Held   []Proposal `json:"held"`
}

var sampleCorpus = Corpus{Episodes: []Episode{
	{ID: "deploy-0412", FamilyID: "deploy-0412", FamilyKind: "local", Outcome: "failed", Text: "User asked to deploy billing-api. The assistant ran the deploy without a database backup; the migration failed halfway and had to be repaired by hand."},
	{ID: "deploy-0415", FamilyID: "deploy-0415", FamilyKind: "local", Outcome: "completed", Text: "User asked to deploy billing-api again. The assistant took a database backup, ran the migration, then checked GET /healthz returned 200 before reporting success. Active users are counted per day as distinct user_id in analytics.events."},
}}

func sampleItems(c Class) []Proposal {
	both := []string{"deploy-0412", "deploy-0415"}
	switch c.Template {
	case "example-skills":
		return []Proposal{
			{ID: "deploy-service", Title: "Deploy a service with a backup and health check", Body: "1. Take a database backup of <service>.\n2. Run the deployment and its migration.\n3. Call GET <service>/healthz and wait for 200.\n4. Report the release version and health result.", EvidenceIDs: []string{"deploy-0415"}, Status: "supported", Attributes: map[string]string{"when_to_use": "Deploying a service that runs database migrations", "verification": "GET /healthz returns 200 after the migration.", "pitfalls": "Skipping the backup leaves no rollback if the migration fails."}},
			{ID: "retry-deploy", Title: "Retry a failed deploy", Body: "Re-run the deploy.", EvidenceIDs: []string{"deploy-0412"}, Status: "supported", Attributes: map[string]string{"when_to_use": "A deploy failed", "verification": "It works."}},
		}
	case "example-corrections":
		return []Proposal{{ID: "backup-before-migration", Title: "Back up before running a migration", Body: "Root cause: the deploy ran a migration with no backup. Rule: take and verify a backup before any migration.", EvidenceIDs: both, Status: "supported", Attributes: map[string]string{"scope": "deployment", "trigger": "A deploy includes a database migration.", "mistaken_assumption": "Migrations are safe to run without a backup.", "corrective_action": "Take a database backup, confirm it completed, then run the migration.", "expected_result": "A failed migration can be rolled back from the backup."}},
			{ID: "repair-by-hand", Title: "Repair failed migrations by hand", Body: "The migration was repaired manually.", EvidenceIDs: []string{"deploy-0412"}, Status: "supported", Attributes: map[string]string{"scope": "deployment", "trigger": "A migration fails halfway.", "mistaken_assumption": "Unknown.", "corrective_action": "Repair the schema by hand.", "expected_result": "The schema is consistent."}},
		}
	case "example-facts":
		return []Proposal{
			{ID: "billing-api-health-endpoint", Title: "billing-api exposes its health check at GET /healthz.", Body: "Use it to confirm a deployment succeeded; it returns 200 when healthy.", EvidenceIDs: []string{"deploy-0415"}, Status: "supported", Attributes: map[string]string{"kind": "fact", "scope": "billing-api"}},
			{ID: "deploys-are-slow", Title: "Deploys are slow.", Body: "Seemed slow once.", EvidenceIDs: []string{"deploy-0412"}, Status: "supported", Attributes: map[string]string{"kind": "fact", "scope": "deployment"}},
		}
	case "example-knowledge":
		return []Proposal{
			{ID: "active-users", Title: "Active users", Body: "Distinct users with at least one event on a calendar day.", EvidenceIDs: []string{"deploy-0415"}, Status: "supported", Attributes: map[string]string{"kind": "metric", "source": "analytics.events", "entity": "user", "unit": "users", "grain": "day", "formula": "count(distinct user_id) grouped by event date"}},
			{ID: "revenue", Title: "Revenue", Body: "Total order value.", EvidenceIDs: []string{"deploy-0415"}, Status: "supported", Attributes: map[string]string{"kind": "metric", "source": "finance", "entity": "order", "formula": "sum(amount)"}},
		}
	case "example-episodes":
		return []Proposal{
			{ID: "deploy-without-backup", Title: "Deploy failed after a migration ran without a backup", Body: "The billing-api migration failed halfway and was repaired by hand.", EvidenceIDs: []string{"deploy-0412"}, Status: "supported", Attributes: map[string]string{"outcome": "completed", "situation": "The user asked to deploy billing-api.", "actions": "Ran the deploy and migration directly, without a backup.", "takeaway": "Back up before migrations."}},
			{ID: "deploy-with-health-check", Title: "Deploy succeeded with a backup and health check", Body: "The deploy completed and was verified.", EvidenceIDs: []string{"deploy-0415"}, Status: "supported", Attributes: map[string]string{"outcome": "completed", "situation": "The user asked to deploy billing-api again.", "actions": "Took a backup, ran the migration, checked GET /healthz returned 200."}},
		}
	}
	return nil
}

// SampleFor runs the real verifier and file builder over the sample proposals,
// so the preview always matches what the deterministic nodes produce.
func SampleFor(template string) (Sample, bool) {
	c, ok := ClassFor(template)
	if !ok {
		return Sample{}, false
	}
	check := verify
	if c.Evidence == EvidenceSingleCase {
		check = func(c Class, corpus Corpus, items []Proposal) ([]Proposal, error) {
			return caseOutcome(c, corpus, items)
		}
	}
	items, err := check(c, sampleCorpus, sampleItems(c))
	if err != nil {
		return Sample{}, false
	}
	result, err := candidate(c, items, nil)
	if err != nil {
		return Sample{}, false
	}
	return Sample{Corpus: sampleCorpus, Files: result.Files, Held: result.Held}, true
}
