package evolution

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func checkSchema[T any](t *testing.T, name string) {
	t.Helper()
	actual, err := JSONSchema[T]()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join("schemas", name+".schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(actual, '\n'), saved) {
		t.Fatalf("%s schema drift: regenerate with go run ./cmd/evolution-schema", name)
	}
}
func TestPublishedSchemaMatchesContracts(t *testing.T) {
	checkSchema[Workflow](t, "workflow-v1")
	checkSchema[Template](t, "template-v1")
	checkSchema[Service](t, "service-v1")
	checkSchema[EvaluationProfile](t, "evaluation-profile-v1")
	checkSchema[OutputCatalogQuery](t, "output-catalog-query-v1")
	checkSchema[OutputCatalogScope](t, "output-catalog-scope-v1")
	checkSchema[OutputCatalogPage](t, "output-catalog-page-v1")
	checkSchema[OutputLibraryDetail](t, "output-library-detail-v1")
	checkSchema[Configuration](t, "configuration-v1")
	checkSchema[Activation](t, "activation-v1")
	checkSchema[Run](t, "run-v1")
	checkSchema[GenerationAssignment](t, "generation-assignment-v1")
	checkSchema[GenerationOutputScope](t, "generation-output-scope-v1")
	checkSchema[GenerationOutput](t, "generation-output-v1")
	checkSchema[Attempt](t, "attempt-v1")
	checkSchema[Event](t, "event-v1")
	checkSchema[Patch](t, "patch-v1")
	checkSchema[RunResultScope](t, "run-result-scope-v1")
	checkSchema[RunResultReceipt](t, "run-result-receipt-v1")
	checkSchema[Branch](t, "branch-v1")
	checkSchema[MergeRequest](t, "merge-request-v1")
	checkSchema[Review](t, "review-v1")
	checkSchema[EvaluationResult](t, "evaluation-result-v1")
	checkSchema[JobSubmission](t, "job-submission-v1")
	checkSchema[RuntimeAllocation](t, "runtime-allocation-v1")
	checkSchema[AccessRequest](t, "access-request-v1")
}
