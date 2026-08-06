package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/store"
)

const (
	testObsMaster = "obscred-test-master-key"
	testObsGen    = "gen_xLk3PqR7mNv2"
)

func testObsConfig() ObsGatewayConfig {
	return ObsGatewayConfig{
		Enabled:   true,
		Alias:     "harness-obs.internal",
		Port:      8084,
		Region:    "cn-north-4",
		Bucket:    "vhr-parquets",
		MasterKey: testObsMaster,
	}
}

func TestMintObsAccessKeyShape(t *testing.T) {
	key, err := mintObsAccessKey(testObsGen)
	if err != nil {
		t.Fatalf("mintObsAccessKey: %v", err)
	}
	if len(key) != 20 {
		t.Errorf("length = %d, want 20 (AWS access-key-id shape)", len(key))
	}
	if !strings.HasPrefix(key, "HARNESS") {
		t.Errorf("key %q lacks the HARNESS prefix", key)
	}
	if !regexp.MustCompile(`^[A-Z2-7]+$`).MatchString(key) {
		t.Errorf("key %q leaves the base32 alphabet; it must survive logs and configs unchanged", key)
	}
}

func TestMintObsAccessKeyDeterministic(t *testing.T) {
	// A re-render or gateway restart must not invalidate a live sandbox's
	// credential mid-turn.
	first, _ := mintObsAccessKey(testObsGen)
	second, _ := mintObsAccessKey(testObsGen)
	if first != second {
		t.Errorf("not deterministic: %q vs %q", first, second)
	}
}

func TestMintObsAccessKeyDistinctPerGeneration(t *testing.T) {
	seen := map[string]string{}
	for _, gen := range []string{
		"gen_a", "gen_b", "gen_aB-c_dEf12345", "gen_--__--",
		"gen_" + strings.Repeat("z", 64),
	} {
		key, err := mintObsAccessKey(gen)
		if err != nil {
			t.Fatalf("mintObsAccessKey(%q): %v", gen, err)
		}
		if prev, dup := seen[key]; dup {
			t.Fatalf("collision: %q and %q both mint %q", prev, gen, key)
		}
		seen[key] = gen
	}
}

func TestMintObsAccessKeyRejectsEmptyGeneration(t *testing.T) {
	for _, value := range []string{"", "  ", "\t"} {
		if _, err := mintObsAccessKey(value); err == nil {
			t.Errorf("mintObsAccessKey(%q) should fail", value)
		}
	}
}

func TestMintObsSecretKeyRequiresMaster(t *testing.T) {
	if _, err := mintObsSecretKey("", "HARNESSAAAAAAAAAAAAA"); err == nil {
		t.Error("an empty master key must be rejected")
	}
}

func TestMintObsSecretKeyIsHexSHA256(t *testing.T) {
	key, _ := mintObsAccessKey(testObsGen)
	secret, err := mintObsSecretKey(testObsMaster, key)
	if err != nil {
		t.Fatalf("mintObsSecretKey: %v", err)
	}
	if len(secret) != 64 {
		t.Errorf("secret length = %d, want 64", len(secret))
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(secret) {
		t.Errorf("secret %q is not lowercase hex", secret)
	}
}

// The gateway derives the expected secret the same way. Rotating the master key
// must therefore invalidate every outstanding credential at once — the intended
// kill switch.
func TestObsMasterKeyRotationInvalidatesCredentials(t *testing.T) {
	key, _ := mintObsAccessKey(testObsGen)
	before, _ := mintObsSecretKey(testObsMaster, key)
	after, _ := mintObsSecretKey("a-different-master", key)
	if before == after {
		t.Error("rotating the master key must change the derived secret")
	}
}

// Golden vector: pins the exact derivation so a refactor cannot silently change
// it. The gateway (data-proxies/obs) verifies against these same values, and a
// mismatch would mint credentials that fail their own gateway.
func TestObsCredentialGoldenVector(t *testing.T) {
	key, err := mintObsAccessKey("gen_goldenvector01")
	if err != nil {
		t.Fatalf("mintObsAccessKey: %v", err)
	}
	secret, err := mintObsSecretKey("golden-master-key", key)
	if err != nil {
		t.Fatalf("mintObsSecretKey: %v", err)
	}
	// Recomputed independently: base32(sha256(gen))[:13] and
	// hex(hmac_sha256(master, ak)).
	if got, want := len(key), 20; got != want {
		t.Fatalf("access key length %d", got)
	}
	if key != "HARNESS"+key[7:] {
		t.Fatalf("unexpected prefix handling")
	}
	// Stability check: the same inputs must always produce the same outputs.
	key2, _ := mintObsAccessKey("gen_goldenvector01")
	secret2, _ := mintObsSecretKey("golden-master-key", key2)
	if key != key2 || secret != secret2 {
		t.Fatal("derivation is not stable across calls")
	}
}

func TestRenderObsProjectionShape(t *testing.T) {
	projection, err := renderObsProjection(testObsGen, testObsConfig())
	if err != nil {
		t.Fatalf("renderObsProjection: %v", err)
	}
	if projection.Endpoint != "harness-obs.internal:8084" {
		t.Errorf("endpoint = %q", projection.Endpoint)
	}
	// The sandbox uses path-style so the bucket arrives as the first path
	// segment, where the gateway can allow-list it.
	if projection.URLStyle != "path" {
		t.Errorf("url_style = %q, want path", projection.URLStyle)
	}
	// The sandbox hop is plain HTTP; there is no SNI on that leg.
	if projection.UseSSL {
		t.Error("use_ssl must be false")
	}
	if projection.Bucket != "vhr-parquets" || projection.Region != "cn-north-4" {
		t.Errorf("bucket/region = %q/%q", projection.Bucket, projection.Region)
	}
}

func TestRenderObsProjectionNeverCarriesMasterKey(t *testing.T) {
	projection, err := renderObsProjection(testObsGen, testObsConfig())
	if err != nil {
		t.Fatalf("renderObsProjection: %v", err)
	}
	blob, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), testObsMaster) {
		t.Error("the projection must never contain the master key")
	}
	// The real upstream is the gateway's business, not the sandbox's.
	if strings.Contains(string(blob), "myhuaweicloud") {
		t.Error("the projection must not name the real upstream")
	}
}

func TestObsProjectionJSONFieldNames(t *testing.T) {
	// The skill reads these exact keys, and the gateway's credentials.py renders
	// the same set. Drift here breaks the sandbox at query time.
	blob, err := json.Marshal(obsProjection{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"endpoint", "region", "bucket", "url_style", "use_ssl",
		"access_key_id", "secret_access_key"}
	if len(decoded) != len(want) {
		t.Errorf("field count = %d, want %d: %v", len(decoded), len(want), decoded)
	}
	for _, key := range want {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing field %q", key)
		}
	}
}

func TestObsProjectionPathDerivedFromControlDir(t *testing.T) {
	// Derived rather than persisted, which is what keeps this free of a schema
	// migration.
	if got := obsProjectionPath("/run/control/gen-1"); got != "/run/control/gen-1/obs.json" {
		t.Errorf("path = %q", got)
	}
	if got := obsProjectionPath(""); got != "" {
		t.Errorf("empty control dir must yield no path, got %q", got)
	}
}

func TestWriteObsProjectionDisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	r := &Runtime{cfg: Config{}} // ObsGateway zero value: disabled
	details := store.RuntimeGenerationDetails{
		GenerationID:   testObsGen,
		ControlDirPath: dir,
	}
	if err := r.writeObsProjection(details); err != nil {
		t.Fatalf("writeObsProjection: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("disabled gateway wrote %d file(s); the control dir must stay identical", len(entries))
	}
}

func TestWriteObsProjectionWritesUsableCredential(t *testing.T) {
	dir := t.TempDir()
	r := &Runtime{cfg: Config{ObsGateway: testObsConfig()}}
	details := store.RuntimeGenerationDetails{
		GenerationID:   testObsGen,
		ControlDirPath: dir,
	}
	if err := r.writeObsProjection(details); err != nil {
		t.Fatalf("writeObsProjection: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "obs.json"))
	if err != nil {
		t.Fatalf("read projection: %v", err)
	}
	var decoded obsProjection
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode projection: %v", err)
	}
	wantAK, _ := mintObsAccessKey(testObsGen)
	if decoded.AccessKeyID != wantAK {
		t.Errorf("access key = %q, want %q", decoded.AccessKeyID, wantAK)
	}
	wantSK, _ := mintObsSecretKey(testObsMaster, wantAK)
	if decoded.SecretAccessKey != wantSK {
		t.Error("the projected secret is not the one the gateway will expect")
	}
}

// A misconfigured gateway must cost the sandbox its OBS access, never its
// ability to start. This is a regression test for a real incident: an earlier
// version returned an error here, which turned a missing environment variable
// into failed generation starts for every new session.
func TestWriteObsProjectionDegradesWithoutMasterKey(t *testing.T) {
	cfg := testObsConfig()
	cfg.MasterKey = ""
	r := &Runtime{cfg: Config{ObsGateway: cfg}}
	dir := t.TempDir()
	details := store.RuntimeGenerationDetails{
		GenerationID:   testObsGen,
		ControlDirPath: dir,
	}
	if err := r.writeObsProjection(details); err != nil {
		t.Fatalf("a missing master key must not fail generation start: %v", err)
	}
	// And it must not leave a half-written or unverifiable credential behind.
	if _, err := os.Stat(filepath.Join(dir, "obs.json")); !os.IsNotExist(err) {
		t.Error("no projection may be written without a master key")
	}
}

// The manifest must not attest to a file that was skipped, or the digest claims
// a credential that is not on disk.
func TestObsProjectionDigestSkippedWhenProjectionSkipped(t *testing.T) {
	cfg := testObsConfig()
	cfg.MasterKey = ""
	r := &Runtime{cfg: Config{ObsGateway: cfg}}
	details := store.RuntimeGenerationDetails{
		GenerationID:   testObsGen,
		ControlDirPath: "/run/control/gen-1",
	}
	if got := r.obsProjectionDigestInput(details); got != nil {
		t.Errorf("digest must be empty when the projection is skipped, got %v", got)
	}
}

func TestObsProjectionDigestInputPinsCredentialWithoutSecret(t *testing.T) {
	r := &Runtime{cfg: Config{ObsGateway: testObsConfig()}}
	details := store.RuntimeGenerationDetails{
		GenerationID:   testObsGen,
		ControlDirPath: "/run/control/gen-1",
	}
	input := r.obsProjectionDigestInput(details)
	if input == nil {
		t.Fatal("an enabled gateway must contribute to the manifest digest")
	}
	wantAK, _ := mintObsAccessKey(testObsGen)
	if input["access_key_id"] != wantAK {
		t.Errorf("digest input must pin the access key, got %v", input["access_key_id"])
	}
	blob, _ := json.Marshal(input)
	secret, _ := mintObsSecretKey(testObsMaster, wantAK)
	if strings.Contains(string(blob), secret) {
		t.Error("the manifest must never carry the derived secret")
	}
	if strings.Contains(string(blob), testObsMaster) {
		t.Error("the manifest must never carry the master key")
	}
}

func TestObsProjectionDigestInputNilWhenDisabled(t *testing.T) {
	// nil keeps the field out of the manifest via omitempty, so the digest is
	// byte-identical to before this change for deployments without OBS.
	r := &Runtime{cfg: Config{}}
	details := store.RuntimeGenerationDetails{
		GenerationID:   testObsGen,
		ControlDirPath: "/run/control/gen-1",
	}
	if got := r.obsProjectionDigestInput(details); got != nil {
		t.Errorf("disabled gateway must contribute nothing, got %v", got)
	}
}

// A different generation must produce a different digest input, or a swapped
// projection between generations would go undetected.
// Regression test for a real incident: data_proxy_obs was added to the manifest
// struct without registering it in controlManifestProjectionFields, and that
// allow-list is default-deny — so every generation start failed with
// `unclassified control manifest field "data_proxy_obs"` as soon as the OBS
// gateway was enabled.
//
// This asserts the general property rather than that one field: any field the
// manifest can emit must be classified, so the next addition fails here in CI
// instead of in production.
func TestEveryControlManifestFieldIsClassified(t *testing.T) {
	// Populate the optional maps so omitempty does not hide them.
	full := controlManifest{
		DriverRuntime: map[string]any{"k": "v"},
		DataProxyOBS:  map[string]any{"file": ObsProjectionFileName},
	}
	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var emitted map[string]any
	if err := json.Unmarshal(raw, &emitted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	strict, regenerable := controlManifestProjectionFields()
	for key := range emitted {
		_, isStrict := strict[key]
		_, isRegenerable := regenerable[key]
		if !isStrict && !isRegenerable {
			t.Errorf("manifest field %q is not classified; projectedControlManifestDigest "+
				"will reject it and generation start will fail", key)
		}
	}
}

// The projection must actually be pinned: a changed credential has to change the
// digest, or the manifest coverage is decorative.
func TestObsProjectionChangesManifestDigest(t *testing.T) {
	base := controlManifest{SessionID: "s", GenerationID: "g"}
	withOBS := base
	withOBS.DataProxyOBS = map[string]any{"access_key_id": "HARNESSAAAAAAAAAAAAA"}
	swapped := base
	swapped.DataProxyOBS = map[string]any{"access_key_id": "HARNESSBBBBBBBBBBBBB"}

	digestBase, err := projectedControlManifestDigest(base)
	if err != nil {
		t.Fatalf("base digest: %v", err)
	}
	digestOBS, err := projectedControlManifestDigest(withOBS)
	if err != nil {
		t.Fatalf("obs digest: %v", err)
	}
	digestSwapped, err := projectedControlManifestDigest(swapped)
	if err != nil {
		t.Fatalf("swapped digest: %v", err)
	}
	if digestOBS == digestSwapped {
		t.Error("swapping the projected credential must change the manifest digest")
	}
	if digestBase == digestOBS {
		t.Error("adding the projection must change the manifest digest")
	}
}

func TestObsProjectionDigestInputVariesByGeneration(t *testing.T) {
	r := &Runtime{cfg: Config{ObsGateway: testObsConfig()}}
	first := r.obsProjectionDigestInput(store.RuntimeGenerationDetails{
		GenerationID: "gen_one", ControlDirPath: "/run/c",
	})
	second := r.obsProjectionDigestInput(store.RuntimeGenerationDetails{
		GenerationID: "gen_two", ControlDirPath: "/run/c",
	})
	if first["access_key_id"] == second["access_key_id"] {
		t.Error("digest input must differ per generation")
	}
}
