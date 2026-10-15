// Command evolution-schema regenerates versioned interchange schema documents.
package main

import (
	"os"
	"path/filepath"

	"aster/contracts/evolution"
)

func write[T any](directory, name string) {
	raw, err := evolution.JSONSchema[T]()
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name+".schema.json"), append(raw, '\n'), 0644); err != nil {
		panic(err)
	}
}
func main() {
	directory := "evolution/schemas"
	if len(os.Args) == 2 {
		directory = os.Args[1]
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		panic(err)
	}
	write[evolution.Workflow](directory, "workflow-v1")
	write[evolution.Template](directory, "template-v1")
	write[evolution.Service](directory, "service-v1")
	write[evolution.EvaluationProfile](directory, "evaluation-profile-v1")
	write[evolution.OutputCatalogQuery](directory, "output-catalog-query-v1")
	write[evolution.OutputCatalogScope](directory, "output-catalog-scope-v1")
	write[evolution.OutputCatalogPage](directory, "output-catalog-page-v1")
	write[evolution.OutputLibraryDetail](directory, "output-library-detail-v1")
	write[evolution.DraftSettings](directory, "draft-settings-v1")
	write[evolution.Configuration](directory, "configuration-v1")
	write[evolution.Activation](directory, "activation-v1")
	write[evolution.Run](directory, "run-v1")
	write[evolution.GenerationAssignment](directory, "generation-assignment-v1")
	write[evolution.GenerationOutputScope](directory, "generation-output-scope-v1")
	write[evolution.GenerationOutput](directory, "generation-output-v1")
	write[evolution.Attempt](directory, "attempt-v1")
	write[evolution.Event](directory, "event-v1")
	write[evolution.Patch](directory, "patch-v1")
	write[evolution.RunResultScope](directory, "run-result-scope-v1")
	write[evolution.RunResultReceipt](directory, "run-result-receipt-v1")
	write[evolution.Branch](directory, "branch-v1")
	write[evolution.MergeRequest](directory, "merge-request-v1")
	write[evolution.Review](directory, "review-v1")
	write[evolution.EvaluationResult](directory, "evaluation-result-v1")
	write[evolution.JobSubmission](directory, "job-submission-v1")
	write[evolution.RuntimeAllocation](directory, "runtime-allocation-v1")
	write[evolution.AccessRequest](directory, "access-request-v1")
}
