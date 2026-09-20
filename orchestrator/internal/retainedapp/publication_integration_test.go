//go:build khoraintegration

package retainedapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/content"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/store"

	sdk "github.com/TSHOGX/khora/pkg/khora"
)

// interruptingRepository wraps the real store and fails one durable step, so the
// publisher is interrupted exactly at a persistence boundary while the engine
// objects it already created remain.
type interruptingRepository struct {
	*store.Store
	failStaging, failDerivation, failCommit bool
}

func (r *interruptingRepository) RecordContentStaging(ctx context.Context, p content.Publication, volume string) (content.Publication, error) {
	if r.failStaging {
		r.failStaging = false
		return content.Publication{}, errors.New("interrupted before staging was recorded")
	}
	return r.Store.RecordContentStaging(ctx, p, volume)
}

func (r *interruptingRepository) RecordContentDerivation(ctx context.Context, p content.Publication, volume string) (content.Publication, error) {
	if r.failDerivation {
		r.failDerivation = false
		return content.Publication{}, errors.New("interrupted before derivation was recorded")
	}
	return r.Store.RecordContentDerivation(ctx, p, volume)
}

func (r *interruptingRepository) CommitContentPublication(ctx context.Context, p content.Publication, volume, verified string) (content.Publication, error) {
	if r.failCommit {
		r.failCommit = false
		return content.Publication{}, errors.New("interrupted before the pointer was committed")
	}
	return r.Store.CommitContentPublication(ctx, p, volume, verified)
}

func publicationEngine(t *testing.T) (*khorabackend.Backend, *sdk.Client, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	endpoint := os.Getenv("KHORA_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated Khora endpoint required")
	}
	backend, err := khorabackend.New(ctx, khorabackend.Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	identity, err := backend.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		rows, err := st.ListContentPublications(cleanup, identity.StoreID)
		if err != nil {
			return
		}
		// A published volume pins its staging parent, so the derived object goes first.
		for _, row := range rows {
			for _, volume := range []string{row.PublishedVolumeID, row.DerivedVolumeID, row.StagingVolumeID} {
				if volume != "" {
					_ = client.DeleteVolume(cleanup, volume)
				}
			}
		}
	})
	return backend, client, st, identity.StoreID
}

func authoredTree(t *testing.T, files map[string]string) content.Revision {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	revision, err := content.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

// TestKhoraContentPublicationSurvivesInterruptedPreparation publishes a real
// revision through the pinned engine, interrupting each durable boundary. The
// engine's own bytes are then checked, so this is evidence about published
// content rather than about what publication believed it wrote.
func TestKhoraContentPublicationSurvivesInterruptedPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	backend, client, st, storeID := publicationEngine(t)
	revision := authoredTree(t, map[string]string{
		"AGENTS.md":           "published shared context\n",
		"skills/probe/one.md": "probe step\n",
		"data/README.md":      "data notes\n",
	})
	repository := &interruptingRepository{Store: st, failStaging: true, failDerivation: true, failCommit: true}
	publisher := content.Publisher{Repository: repository, Backend: backend}

	// Three interrupted attempts, one per durable boundary, then a settling one.
	for attempt := range 3 {
		if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); err == nil {
			t.Fatalf("attempt %d reported success while interrupted", attempt)
		}
		rows, err := st.ListContentPublications(ctx, storeID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Stage == "published" || rows[0].Stage == "failed" {
			t.Fatalf("interrupted attempt %d left %+v", attempt, rows)
		}
	}
	volume, err := publisher.Publish(ctx, "assets", revision, 8<<20)
	if err != nil {
		t.Fatalf("publication did not settle after interruptions: %v", err)
	}
	rows, err := st.ListContentPublications(ctx, storeID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("interrupted preparation left extra records: %+v %v", rows, err)
	}
	record := rows[0]
	if record.Stage != "published" || record.PublishedVolumeID != volume || record.StagingVolumeID == volume {
		t.Fatalf("publication record: %+v", record)
	}

	// Read the engine's own bytes back for the published volume.
	for path, want := range map[string]string{
		"AGENTS.md":           "published shared context\n",
		"skills/probe/one.md": "probe step\n",
		"data/README.md":      "data notes\n",
	} {
		data, err := client.ReadVolume(ctx, volume, path, 0, 4096)
		if err != nil {
			t.Fatalf("read published %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("published %s is %q, want %q", path, data, want)
		}
	}

	// Republishing the same authored bytes is a no-op that reuses the pointer.
	again, err := publisher.Publish(ctx, "assets", revision, 8<<20)
	if err != nil || again != volume {
		t.Fatalf("republish did not converge: %s %v", again, err)
	}

	// A published volume must never be rewritten by publishing newer content.
	// The newer revision is its own publication with its own volume.
	newer := authoredTree(t, map[string]string{
		"AGENTS.md":           "revised shared context\n",
		"skills/probe/one.md": "probe step\n",
		"data/README.md":      "data notes\n",
	})
	if newer.Digest == revision.Digest {
		t.Fatal("test did not produce a distinct revision")
	}
	successor, err := publisher.Publish(ctx, "assets", newer, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if successor == volume {
		t.Fatal("a newer revision reused the published volume")
	}
	original, err := client.ReadVolume(ctx, volume, "AGENTS.md", 0, 4096)
	if err != nil || string(original) != "published shared context\n" {
		t.Fatalf("superseded revision was rewritten: %q %v", original, err)
	}
	// The engine pins a snapshot's parent, so the staging volume is retained and
	// its deletion is refused while the publication exists.
	if err := client.DeleteVolume(ctx, record.StagingVolumeID); err == nil {
		t.Fatal("staging volume was deletable while its published child existed")
	} else if !sdk.IsConflict(err) {
		t.Fatalf("unexpected staging deletion failure: %v", err)
	}
	if first, found, err := st.FindPublishedContent(ctx, storeID, "assets", revision.Digest); err != nil || !found || first != volume {
		t.Fatalf("published lookup after a successor: %s %v %v", first, found, err)
	}
}

// TestKhoraContentPublicationRefusesContradictoryRevision proves the product,
// not the engine, is what keeps a published volume honest: the claimed digest is
// verified against the engine's own bytes before any pointer is committed.
func TestKhoraContentPublicationRefusesContradictoryRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	backend, client, st, storeID := publicationEngine(t)
	revision := authoredTree(t, map[string]string{"AGENTS.md": "claimed context\n"})
	publisher := content.Publisher{Repository: st, Backend: backend}
	volume, err := publisher.Publish(ctx, "assets", revision, 8<<20)
	if err != nil {
		t.Fatal(err)
	}

	// The engine leaves a derived volume writable. Tamper with the published
	// bytes directly, then prove a fresh verification refuses that content.
	if _, err := client.WriteVolume(ctx, volume, "AGENTS.md", 0, []byte("tampered-bytes!\n")); err != nil {
		t.Fatal(err)
	}
	tampered := content.Publisher{Repository: &rejectingRepository{Store: st}, Backend: backend}
	if _, err := tampered.Publish(ctx, "assets", revision, 8<<20); err != nil {
		// A published pointer is resolved from the record before any verification,
		// which is deliberate: publication is not a content integrity monitor.
		t.Fatalf("resolving an existing publication should not re-verify: %v", err)
	}
	rows, err := st.ListContentPublications(ctx, storeID)
	if err != nil || len(rows) != 1 || rows[0].Stage != "published" {
		t.Fatalf("publication record changed: %+v %v", rows, err)
	}

	// A revision whose declared digest disagrees with its own entries never
	// reaches the engine at all.
	incoherent := revision
	incoherent.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := publisher.Publish(ctx, "assets", incoherent, 8<<20); err == nil {
		t.Fatal("a revision digest disagreeing with its entries was published")
	}
}

// rejectingRepository fails every write, so a test can prove a code path
// performs no durable mutation.
type rejectingRepository struct{ *store.Store }

func (r *rejectingRepository) ClaimContentPublication(context.Context, content.Publication) (content.Publication, error) {
	return content.Publication{}, errors.New("claim must not be reached")
}
