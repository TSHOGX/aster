package schemastate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// embeddedFingerprint returns one fingerprint/label pair the compiled-in catalog
// already reviews, so the conflict test does not hardcode a hash that a future
// baseline regeneration would invalidate.
func embeddedFingerprint(t *testing.T) (kind, fingerprint, label string) {
	t.Helper()
	var embedded map[string]map[string]string
	if err := json.Unmarshal(baselineJSON, &embedded); err != nil {
		t.Fatalf("unmarshal embedded baselines: %v", err)
	}
	for kind, entries := range embedded {
		for fingerprint, label := range entries {
			return kind, fingerprint, label
		}
	}
	t.Fatal("embedded baselines are empty")
	return "", "", ""
}

func writeLocal(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema-baselines.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write local baselines: %v", err)
	}
	return path
}

func TestLoadCatalogWithoutLocalFileUsesEmbedded(t *testing.T) {
	t.Setenv(BaselinesPathEnv, "")
	catalog, err := loadCatalog()
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	kind, fingerprint, label := embeddedFingerprint(t)
	entry, ok := catalog[kind][fingerprint]
	if !ok {
		t.Fatalf("embedded fingerprint %s missing from catalog", fingerprint)
	}
	if entry.label != label || entry.source != "embedded" {
		t.Fatalf("got label=%q source=%q, want label=%q source=embedded", entry.label, entry.source, label)
	}
}

// A host-local file may introduce a fingerprint upstream has never seen. This is
// the whole point of the overlay: a production database carrying local schema
// history can be adopted without editing a tracked file.
func TestLoadCatalogLocalEntryWidensCatalog(t *testing.T) {
	t.Setenv(BaselinesPathEnv, writeLocal(t, `{"store":{"aaaa":"target-site-local"}}`))
	catalog, err := loadCatalog()
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	entry, ok := catalog["store"]["aaaa"]
	if !ok {
		t.Fatal("local fingerprint missing from catalog")
	}
	if entry.label != "target-site-local" || entry.source != "local" {
		t.Fatalf("got label=%q source=%q, want label=target-site-local source=local", entry.label, entry.source)
	}
	// Widening must not drop what upstream already reviewed.
	kind, fingerprint, label := embeddedFingerprint(t)
	if got := catalog[kind][fingerprint]; got.label != label {
		t.Fatalf("embedded entry lost: got %q, want %q", got.label, label)
	}
}

// A local file must never relabel a fingerprint upstream has already judged:
// relabelling a pre-migration fingerprint to "target-…" would satisfy the
// HasPrefix check in Check and mask an unmigrated database.
func TestLoadCatalogEmbeddedWinsOnConflict(t *testing.T) {
	kind, fingerprint, label := embeddedFingerprint(t)
	body, err := json.Marshal(map[string]map[string]string{
		kind: {fingerprint: "target-attacker-relabel"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Setenv(BaselinesPathEnv, writeLocal(t, string(body)))
	catalog, err := loadCatalog()
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	entry := catalog[kind][fingerprint]
	if entry.label != label {
		t.Fatalf("local file overrode embedded verdict: got %q, want %q", entry.label, label)
	}
	if entry.source != "embedded" {
		t.Fatalf("got source=%q, want embedded", entry.source)
	}
}

// A configured path that cannot be read or parsed must fail loudly. Falling back
// to the embedded catalog would turn a unit-file typo into a migration refusal
// whose message points at the schema rather than at the typo.
func TestLoadCatalogRefusesUnusableLocalFile(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv(BaselinesPathEnv, filepath.Join(t.TempDir(), "absent.json"))
		if _, err := loadCatalog(); err == nil {
			t.Fatal("expected an error for a missing configured file")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		t.Setenv(BaselinesPathEnv, writeLocal(t, `{"store":`))
		if _, err := loadCatalog(); err == nil {
			t.Fatal("expected an error for a malformed configured file")
		}
	})
}
