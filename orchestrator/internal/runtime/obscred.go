package runtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/store"
)

// Fake OBS credentials for the sandbox (data-proxies/obs/PLAN.md §7.5).
//
// The real Huawei OBS AK/SK never enter the sandbox. Each generation gets a
// *fake* pair plus a hostname alias; the host-side gateway verifies the inbound
// SigV4 with the fake secret, authorizes the request against that sandbox's live
// turn by source IP, enforces read-only, and re-signs with the real credentials.
// Same boundary shape as the model proxy, extended to the data plane.
//
// The secret is DERIVED, never stored:
//
//	fake_ak = "HARNESS" + base32(sha256(generation_id))[:13]
//	fake_sk = hex(HMAC-SHA256(master_key, fake_ak))
//
// so the gateway recomputes the expected secret from whatever AK the client
// presents — no shared table, no cache, nothing to expire, and no schema change
// here. Rotating the master key invalidates every outstanding fake credential at
// once, which is the intended kill switch.
//
// This file is the issuing half. The verifying half is the gateway's sigv4.py /
// credentials.py; data-proxies/obs/phase2/obscred has tests asserting the two
// agree byte for byte, because a mismatch would mint credentials that fail their
// own gateway.
//
// Neither value is a real secret, so writing them into the existing read-only
// control-dir projection is not a "secret mount" in the contract's sense (the
// entrypoint's exit-65 rejection is about real provider credentials). A leaked
// fake pair is useless off-box: the gateway additionally requires the request to
// arrive from the sandbox IP that owns a live turn.

const (
	obsAccessKeyLength = 20
	obsAccessKeyPrefix = "HARNESS"
	obsDerivedLength   = obsAccessKeyLength - len(obsAccessKeyPrefix)

	// ObsProjectionFileName is the file the rewritten skill reads, inside the
	// control dir that is already bind-mounted read-only at /harness-control.
	// Deriving the path from ControlDirPath rather than persisting it is what
	// keeps this free of a schema migration.
	ObsProjectionFileName = "obs.json"
)

// ObsGatewayConfig is the deployment config for the OBS data proxy. Like
// SandboxDataProxyAliases it is sourced from harness.data_proxies at startup, not
// persisted per generation.
type ObsGatewayConfig struct {
	// Enabled gates the whole feature. When false nothing is written and the
	// control dir is byte-identical to before this change.
	Enabled bool
	// Alias and Port are what the sandbox connects to (the /etc/hosts alias).
	Alias string
	Port  int
	// Region and Bucket are the upstream coordinates the skill needs.
	Region string
	Bucket string
	// MasterKey derives each generation's fake secret. It MUST match the
	// gateway's OBS_GATEWAY_MASTER_KEY or every signature fails verification.
	// A secret: never log it, never put it in harness.yaml.
	MasterKey string
}

// obsProjection is the /harness-control/obs.json payload.
//
// Field names and order match the gateway's credentials.py render_projection, so
// the artifact is identical whichever side writes it.
type obsProjection struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	URLStyle        string `json:"url_style"`
	UseSSL          bool   `json:"use_ssl"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// mintObsAccessKey derives the fake access key ID for a generation.
//
// Deterministic, so re-rendering the projection or restarting the gateway does
// not invalidate a sandbox's credential mid-turn. The generation id is hashed
// rather than embedded because ids are base64url and carry characters outside the
// access-key alphabet; the hash is an encoding, not a secret.
func mintObsAccessKey(generationID string) (string, error) {
	generationID = strings.TrimSpace(generationID)
	if generationID == "" {
		return "", fmt.Errorf("obs credential: generation id is required")
	}
	sum := sha256.Sum256([]byte(generationID))
	encoded := strings.TrimRight(base32.StdEncoding.EncodeToString(sum[:]), "=")
	if len(encoded) < obsDerivedLength {
		return "", fmt.Errorf("obs credential: derived token is too short")
	}
	return obsAccessKeyPrefix + encoded[:obsDerivedLength], nil
}

// mintObsSecretKey returns hex(HMAC-SHA256(masterKey, accessKey)).
func mintObsSecretKey(masterKey, accessKey string) (string, error) {
	if masterKey == "" {
		return "", fmt.Errorf("obs credential: master key is required")
	}
	mac := hmac.New(sha256.New, []byte(masterKey))
	mac.Write([]byte(accessKey))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// obsProjectionPath is where the projection lands for a generation.
func obsProjectionPath(controlDirPath string) string {
	if strings.TrimSpace(controlDirPath) == "" {
		return ""
	}
	return filepath.Join(controlDirPath, ObsProjectionFileName)
}

// renderObsProjection builds the payload for one generation.
func renderObsProjection(generationID string, cfg ObsGatewayConfig) (obsProjection, error) {
	accessKey, err := mintObsAccessKey(generationID)
	if err != nil {
		return obsProjection{}, err
	}
	secretKey, err := mintObsSecretKey(cfg.MasterKey, accessKey)
	if err != nil {
		return obsProjection{}, err
	}
	return obsProjection{
		Endpoint: fmt.Sprintf("%s:%d", cfg.Alias, cfg.Port),
		Region:   cfg.Region,
		Bucket:   cfg.Bucket,
		// The SANDBOX uses path-style, so the bucket arrives at the gateway as
		// the first path segment where it can be allow-listed, and the bucket
		// name stays out of /etc/hosts. The gateway talks vhost upstream; that
		// is its concern, not the sandbox's.
		URLStyle: "path",
		// The sandbox hop is plain HTTP, so there is no SNI on that leg. The
		// upstream leg is HTTPS.
		UseSSL:          false,
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	}, nil
}

// writeObsProjection renders the fake-credential projection for a generation.
//
// A no-op when the gateway is disabled or unconfigured, so a deployment without
// harness.data_proxies.obs behaves exactly as before.
//
// DEGRADES rather than fails. A misconfigured OBS gateway must cost the sandbox
// its OBS access, never its ability to start: this is one optional data source,
// and taking the whole session down for it is the wrong trade. An earlier version
// returned an error when the master key was missing, on the reasoning that a
// sandbox holding an unverifiable credential surfaces later as a confusing
// signing failure. That reasoning was right about the confusion and wrong about
// the remedy — it turned a missing config value into failed generation starts.
// The remedy is a loud log plus no projection: OBS queries fail with a clear
// "file not found" in the skill, and everything else works.
//
// NOTE: the caller must include this file in the control-manifest digest.
// Without that the fake credential is unpinned and a tampered projection is
// undetectable — see obsProjectionDigestInput.
func (r *Runtime) writeObsProjection(details store.RuntimeGenerationDetails) error {
	cfg := r.cfg.ObsGateway
	if !cfg.Enabled {
		return nil
	}
	path := obsProjectionPath(details.ControlDirPath)
	if path == "" {
		return nil
	}
	if cfg.MasterKey == "" {
		// Loud, actionable, and non-fatal. The operator sees exactly what to fix;
		// the sandbox starts without OBS access rather than not at all.
		slog.Error("obs gateway is enabled but OBS_GATEWAY_MASTER_KEY is empty; "+
			"skipping the credential projection, so OBS reads will fail in this "+
			"sandbox. Set it in the orchestrator's environment to the same value "+
			"the gateway uses.",
			"generation_id", details.GenerationID)
		return nil
	}
	payload, err := renderObsProjection(details.GenerationID, cfg)
	if err != nil {
		// Same reasoning: a derivation failure is a bug worth shouting about, but
		// not worth refusing to start the sandbox over.
		slog.Error("obs credential projection could not be rendered; skipping it",
			"generation_id", details.GenerationID, "error", err)
		return nil
	}
	// 0644 matches the other control-dir artifacts. The directory is bind-mounted
	// read-only into the sandbox, and the contents are not real secrets.
	//
	// A write failure here IS returned: unlike a config gap, it means the control
	// dir is not in the state the manifest digest will claim, and the digest
	// covers this file.
	if err := writeJSONFileAtomic(path, payload, 0o644); err != nil {
		return fmt.Errorf("write obs credential projection: %w", err)
	}
	return nil
}

// obsProjectionDigestInput returns the digest preimage for the projection, or an
// empty string when the gateway is disabled.
//
// Covering the projection by the control-manifest digest is what makes a tampered
// fake credential detectable. The generation id and config are digested rather
// than the secret itself, so the manifest never carries credential material.
func (r *Runtime) obsProjectionDigestInput(details store.RuntimeGenerationDetails) map[string]any {
	cfg := r.cfg.ObsGateway
	if !cfg.Enabled || obsProjectionPath(details.ControlDirPath) == "" {
		return nil
	}
	// Must mirror writeObsProjection's skip conditions exactly. If the projection
	// was skipped, the manifest must not claim a credential that is not on disk —
	// otherwise the digest attests to a file that does not exist.
	if cfg.MasterKey == "" {
		return nil
	}
	accessKey, err := mintObsAccessKey(details.GenerationID)
	if err != nil {
		return nil
	}
	return map[string]any{
		"file": ObsProjectionFileName,
		// The AK is public by nature (it is an access key ID, not a secret) and
		// pinning it detects a swapped credential. The SK is deliberately absent.
		"access_key_id": accessKey,
		"endpoint":      fmt.Sprintf("%s:%d", cfg.Alias, cfg.Port),
		"region":        cfg.Region,
		"bucket":        cfg.Bucket,
		"url_style":     "path",
		"use_ssl":       false,
	}
}
