package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/schemastate"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Operation struct {
	Source         SourceIdentity `json:"source"`
	ID             string         `json:"id"`
	Target         string         `json:"target"`
	Release        string         `json:"release"`
	ManifestDigest string         `json:"manifest_digest"`
	TargetDigest   string         `json:"target_digest"`
	Install        bool           `json:"install"`
	Step           string         `json:"step"`
	Completed      []string       `json:"completed"`
	Started        string         `json:"started"`
	Updated        string         `json:"updated"`
	Error          string         `json:"error,omitempty"`
	Previous       string         `json:"previous"`
	PreviousEnv    []byte         `json:"previous_env,omitempty"`
	Backup         string         `json:"backup"`
	BackupSHA256   string         `json:"backup_sha256"`
	TrafficOpened  bool           `json:"traffic_opened"`
	ExternalActive []string       `json:"external_active"`
	RestoredPaths  []string       `json:"restored_paths,omitempty"`
}
type Engine struct {
	Target Target
	Bundle Bundle
	Runner Runner
}

func (e Engine) operationPath(id string) string {
	return filepath.Join(e.Target.Operations, id+".json")
}
func (e Engine) save(o *Operation) error {
	o.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	return WriteJSON(e.operationPath(o.ID), o)
}
func healthClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func probe(ctx context.Context, c *http.Client, address, version string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return err
	}
	r, err := c.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("health probe returned %d", r.StatusCode)
	}
	if version != "" {
		var h struct {
			Version string `json:"version"`
		}
		if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&h); err != nil {
			return err
		}
		if h.Version != version {
			return fmt.Errorf("health version mismatch: got %s expected %s", h.Version, version)
		}
	}
	return nil
}
func (e Engine) run(ctx context.Context, args ...string) (string, error) {
	return e.Runner.Run(ctx, args, nil)
}
func (e Engine) env() map[string]string {
	return e.Target.Env(filepath.Join(e.Target.Rootfs, e.Bundle.Manifest.Sandbox.Archive.SHA256))
}
func (e Engine) exe() string {
	return filepath.Join(e.Target.Releases, e.Bundle.Manifest.Version, "backend", "bin", "orchestrator")
}
func (e Engine) Lock() (func(), error) {
	if !filepath.IsAbs(e.Target.Current) || !filepath.IsAbs(e.Target.Operations) {
		return nil, fmt.Errorf("current and operations must be absolute paths")
	}
	if err := os.MkdirAll(e.Target.Operations, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(e.Target.Current), 0755); err != nil {
		return nil, err
	}
	var files []*os.File
	unlock := func() {
		for _, f := range files {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}
	}
	// Lock the install destination as well as its journal. Two configurations
	// cannot bypass serialization by choosing different operation directories.
	for _, path := range []string{e.Target.Current + ".harness-deploy-lock", filepath.Join(e.Target.Operations, ".lock")} {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			unlock()
			return nil, err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			unlock()
			return nil, fmt.Errorf("another deployment is active")
		}
		files = append(files, f)
	}
	return unlock, nil
}
func (e Engine) Start(ctx context.Context, install bool) (*Operation, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("upgrade/install require root")
	}
	if err := e.Target.Validate(); err != nil {
		return nil, err
	}
	unlock, err := e.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	files, err := filepath.Glob(filepath.Join(e.Target.Operations, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range files {
		var prior Operation
		if err = Decode(p, &prior); err != nil {
			return nil, err
		}
		if prior.Step != "complete" && prior.Step != "rolled_back" && prior.Step != "aborted" {
			return nil, fmt.Errorf("unfinished operation %s: use resume or rollback", prior.ID)
		}
	}
	check := Check(ctx, e.Target, e.Bundle, install, e.Runner)
	if !check.OK {
		return nil, fmt.Errorf("preflight failed: %s", strings.Join(check.Errors, "; "))
	}
	o := &Operation{ID: time.Now().UTC().Format("20060102T150405.000000000"), Target: e.Target.ID(), Release: e.Bundle.Manifest.Version, ManifestDigest: e.Bundle.Digest, Install: install, Step: "prepared", Completed: []string{}, Started: time.Now().UTC().Format(time.RFC3339Nano)}
	if !install {
		o.Source, err = InspectSource(ctx, e.Target, e.Runner)
		if err != nil {
			return nil, err
		}
	}
	o.TargetDigest = e.Target.Digest()
	if previous, err := os.Readlink(e.Target.Current); err == nil {
		o.Previous = previous
	} else if !os.IsNotExist(err) {
		return nil, err
	} else if !install {
		o.Previous = e.Target.PreviousRelease
		if o.Previous == "" {
			return nil, fmt.Errorf("legacy adoption requires previous_release directory with backend/frontend layout")
		}
	}
	o.PreviousEnv, err = os.ReadFile(filepath.Join(filepath.Dir(e.Target.Config), "runtime.env"))
	if err != nil && (!os.IsNotExist(err) || !install) {
		return nil, fmt.Errorf("previous runtime.env must be preserved: %w", err)
	}
	for _, unit := range e.Target.ExternalWriters {
		out, err := e.run(ctx, "systemctl", "is-active", unit)
		if err == nil && strings.TrimSpace(out) == "active" {
			o.ExternalActive = append(o.ExternalActive, unit)
		}
	}
	if err = e.save(o); err != nil {
		return nil, err
	}
	return o, e.advance(ctx, o)
}
func (e Engine) Resume(ctx context.Context, id string) (*Operation, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("resume requires root")
	}
	unlock, err := e.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var o Operation
	if err = Decode(e.operationPath(id), &o); err != nil {
		return nil, err
	}
	if o.TargetDigest != e.Target.Digest() || o.ManifestDigest != e.Bundle.Digest || o.Target != e.Target.ID() || o.Release != e.Bundle.Manifest.Version {
		return nil, fmt.Errorf("operation configuration/release changed")
	}
	if o.Step == "rolled_back" || o.Step == "aborted" {
		return nil, fmt.Errorf("operation already closed")
	}
	if o.TrafficOpened && o.Step != "complete" {
		return &o, fmt.Errorf("traffic may have opened; inspect production before manual completion, do not replay upgrade")
	}
	return &o, e.advance(ctx, &o)
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func (e Engine) advance(ctx context.Context, o *Operation) error {
	steps := []string{"stage", "maintenance", "drain", "stop", "backup", "migrate", "switch", "verify", "traffic", "background"}
	for _, step := range steps {
		if contains(o.Completed, step) {
			continue
		}
		o.Step = step
		o.Error = ""
		if err := e.save(o); err != nil {
			return err
		}
		if err := e.action(ctx, o, step); err != nil {
			o.Error = err.Error()
			_ = e.save(o)
			return err
		}
		o.Completed = append(o.Completed, step)
		if err := e.save(o); err != nil {
			return err
		}
	}
	o.Step = "complete"
	return e.save(o)
}
func (e Engine) stopped(ctx context.Context) error {
	for _, unit := range append([]string{e.Target.BackendService, e.Target.FrontendService}, e.Target.ExternalWriters...) {
		out, err := e.run(ctx, "systemctl", "show", unit, "-p", "ActiveState", "--value")
		if err != nil {
			return err
		}
		state := strings.TrimSpace(out)
		if state != "inactive" && state != "failed" {
			return fmt.Errorf("writer %s not stopped (%s)", unit, state)
		}
	}
	_, err := e.Runner.Run(ctx, e.Target.ExternalIdleCommand, nil)
	return err
}
func (e Engine) action(ctx context.Context, o *Operation, step string) error {
	i := e.Target
	if step != "stage" && step != "maintenance" && step != "background" {
		if raw, err := os.ReadFile(i.MaintenanceFile); err != nil || string(raw) != o.ID {
			return fmt.Errorf("maintenance marker lost or changed; refusing to resume")
		}
	}
	switch step {
	case "stage":
		return e.Bundle.Stage(i.Releases, i.Rootfs)
	case "maintenance":
		for _, p := range []string{i.MaintenanceFile, i.DrainFile, i.BackgroundHoldFile} {
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				return err
			}
			if err := os.Chmod(filepath.Dir(p), 0755); err != nil {
				return err
			}
		}
		// Existing maintenance is not silently claimed by this operation.
		if !contains(o.Completed, "maintenance") {
			if _, err := os.Stat(i.MaintenanceFile); err == nil {
				raw, _ := os.ReadFile(i.MaintenanceFile)
				if string(raw) != o.ID {
					return fmt.Errorf("maintenance belongs to another operation")
				}
			}
		}
		if _, err := e.run(ctx, "nginx", "-t"); err != nil {
			return err
		}
		return AtomicWrite(i.MaintenanceFile, []byte(o.ID), 0644)
	case "drain":
		if err := AtomicWrite(i.DrainFile, []byte(o.ID), 0644); err != nil {
			return err
		}
		if o.Install {
			return nil
		}
		// Timers are stopped before idle checks; already-running external workers
		// must finish on their own. Their site-specific probe is mandatory.
		for _, unit := range i.ExternalWriters {
			if strings.HasSuffix(unit, ".timer") {
				if _, err := e.run(ctx, "systemctl", "stop", unit); err != nil {
					return err
				}
			}
		}
		deadline := time.Now().Add(time.Duration(i.DrainSeconds) * time.Second)
		for {
			if err := e.idle(ctx); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				_ = os.Remove(i.DrainFile)
				_ = os.Remove(i.MaintenanceFile)
				for _, unit := range o.ExternalActive {
					if strings.HasSuffix(unit, ".timer") {
						_, _ = e.run(ctx, "systemctl", "start", unit)
					}
				}
				o.Step = "aborted"
				return fmt.Errorf("drain timed out; user work was not canceled")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	case "stop":
		if !o.Install {
			if _, err := e.Runner.Run(ctx, i.ColdStartCommand, nil); err != nil {
				return err
			}
		}
		units := append([]string{i.FrontendService, i.BackendService}, i.ExternalWriters...)
		if o.Install {
			units = i.ExternalWriters
		}
		for _, unit := range units {
			if _, err := e.run(ctx, "systemctl", "stop", unit); err != nil {
				return err
			}
		}
		if err := e.stopped(ctx); err != nil {
			return err
		}
		return AtomicWrite(i.BackgroundHoldFile, []byte(o.ID), 0644)
	case "backup":
		if err := e.stopped(ctx); err != nil {
			return err
		}
		return e.backup(ctx, o)
	case "migrate":
		if err := e.stopped(ctx); err != nil {
			return err
		}
		args := []string{e.exe(), "db", "migrate", "--config", i.Config}
		if o.Install {
			args = append(args, "--resume-initialize")
		}
		_, err := e.Runner.Run(ctx, args, e.env())
		return err
	case "switch":
		if o.Install {
			if err := e.InstallUnits(ctx); err != nil {
				return err
			}
		}
		if err := e.stopped(ctx); err != nil {
			return err
		}
		if err := writeEnvironment(filepath.Join(filepath.Dir(i.Config), "runtime.env"), e.env()); err != nil {
			return err
		}
		return point(i.Current, filepath.Join(i.Releases, o.Release))
	case "verify":
		// Require maintenance before restarting a partially verified release.
		if _, err := os.Stat(i.MaintenanceFile); err != nil {
			return fmt.Errorf("maintenance marker missing")
		}
		// HTTP writes are permitted for the controlled smoke, while nginx remains
		// closed and scheduler/nightly remain on the separate background hold.
		if err := os.Remove(i.DrainFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, unit := range []string{i.BackendService, i.FrontendService} {
			if _, err := e.run(ctx, "systemctl", "start", unit); err != nil {
				return err
			}
		}
		deadline := time.Now().Add(60 * time.Second)
		for {
			err := probe(ctx, healthClient(), i.BackendHealth, o.Release)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if err := e.verifyCommit(ctx); err != nil {
			return err
		}
		if err := probe(ctx, healthClient(), i.FrontendHealth, ""); err != nil {
			return err
		}
		_, err := e.Runner.Run(ctx, i.SmokeCommand, e.env())
		return err
	case "traffic":
		// Persist the uncertainty BEFORE the irreversible exposure. A crash here
		// must not let rollback silently discard possible new user writes.
		o.TrafficOpened = true
		if err := e.save(o); err != nil {
			return err
		}
		return os.Remove(i.MaintenanceFile)
	case "background":
		if err := os.Remove(i.BackgroundHoldFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, unit := range o.ExternalActive {
			if _, err := e.run(ctx, "systemctl", "start", unit); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown step %s", step)
}
func (e Engine) idle(ctx context.Context) error {
	c, err := e.Target.AppConfig()
	if err != nil {
		return err
	}
	db, err := schemastate.ReadOnly(c.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	query := `SELECT count(*) FROM turns WHERE status IN ('leased','running','queued')`
	var parked int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('turns') WHERE name='auto_dispatch'`).Scan(&parked)
	if parked > 0 {
		query += ` AND (status!='queued' OR auto_dispatch=1)`
	}
	if err = db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%d turns remain", n)
	}
	var schedules int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='schedule_runs'`).Scan(&schedules); err != nil {
		return err
	}
	if schedules > 0 {
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM schedule_runs WHERE status IN ('queued','running')`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("schedule runs remain")
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", e.Target.BackendHealth, nil)
	r, err := healthClient().Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var h struct {
		Active   int64 `json:"background_active"`
		Draining *bool `json:"draining"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&h); err != nil {
		return err
	}
	if h.Active > 0 {
		return fmt.Errorf("nightly pipeline still active")
	}
	if h.Draining != nil && !*h.Draining {
		return fmt.Errorf("backend has not observed drain")
	}
	_, err = e.Runner.Run(ctx, e.Target.ExternalIdleCommand, nil)
	return err
}
func writeEnvironment(path string, env map[string]string) error {
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "%s=%q\n", k, v)
	}
	return AtomicWrite(path, []byte(b.String()), 0600)
}
func point(path, target string) error {
	if !filepath.IsAbs(target) {
		return fmt.Errorf("release target must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (e Engine) verifyCommit(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.Target.BackendHealth, nil)
	if err != nil {
		return err
	}
	r, err := healthClient().Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var h struct {
		Commit string `json:"commit"`
		Held   bool   `json:"background_held"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&h); err != nil {
		return err
	}
	if h.Commit != e.Bundle.Manifest.Commit || !h.Held {
		return fmt.Errorf("backend commit or background hold mismatch")
	}
	return nil
}
