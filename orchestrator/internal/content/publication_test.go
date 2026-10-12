package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

// fakeEngine models the measured engine contract: keyed create and snapshot are
// idempotent, a snapshot is content-isolated from later writes to its source, and
// a derived volume stays writable so immutability is the product's obligation.
type fakeEngine struct {
	store    string
	volumes  map[string]map[string][]byte
	dirs     map[string]map[string]bool
	keys     map[string]string
	next     int
	failCall map[string]error
	calls    map[string]int
	// corrupt makes the engine hold bytes that differ from what was written, which
	// is what verification exists to catch.
	corrupt map[string][]byte
}

func newEngine() *fakeEngine {
	return &fakeEngine{store: "store_a", volumes: map[string]map[string][]byte{}, dirs: map[string]map[string]bool{}, keys: map[string]string{}, failCall: map[string]error{}, calls: map[string]int{}, corrupt: map[string][]byte{}}
}

func (e *fakeEngine) fail(name string) error {
	e.calls[name]++
	if err, ok := e.failCall[name]; ok {
		delete(e.failCall, name)
		return err
	}
	return nil
}

func (e *fakeEngine) Identity(context.Context) (sandbox.Identity, error) {
	return sandbox.Identity{StoreID: e.store, Version: "test", ManualColdRecovery: true}, nil
}

func (e *fakeEngine) CreateVolume(_ context.Context, key string, capacity int64) (sandbox.Volume, error) {
	if err := e.fail("create:" + key); err != nil {
		return sandbox.Volume{}, err
	}
	if existing, ok := e.keys[key]; ok {
		return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: e.store, VolumeID: existing}, Capacity: capacity}, nil
	}
	e.next++
	id := fmt.Sprintf("vol_%d", e.next)
	e.keys[key] = id
	e.volumes[id] = map[string][]byte{}
	e.dirs[id] = map[string]bool{}
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: e.store, VolumeID: id}, Capacity: capacity}, nil
}

func (e *fakeEngine) MkdirVolume(_ context.Context, reference sandbox.VolumeReference, name string) error {
	if err := e.fail("mkdir:" + name); err != nil {
		return err
	}
	if e.dirs[reference.VolumeID] == nil {
		return fmt.Errorf("unknown volume")
	}
	// The port contract is idempotent even though the engine verb answers 409 for
	// an existing path; the Khora adapter absorbs that, and its own tests pin it.
	// An existing entry of the wrong type is still a conflict.
	if _, file := e.volumes[reference.VolumeID][name]; file {
		return &sandbox.Error{Class: sandbox.Conflict, Operation: "volume mkdir", Cause: fmt.Errorf("%q already exists as a file", name)}
	}
	e.dirs[reference.VolumeID][name] = true
	return nil
}

func (e *fakeEngine) WriteVolume(_ context.Context, reference sandbox.VolumeReference, name string, data []byte) error {
	if err := e.fail("write:" + name); err != nil {
		return err
	}
	files := e.volumes[reference.VolumeID]
	if files == nil {
		return fmt.Errorf("unknown volume")
	}
	if replacement, ok := e.corrupt[name]; ok {
		files[name] = append([]byte(nil), replacement...)
		return nil
	}
	files[name] = append([]byte(nil), data...)
	return nil
}

func (e *fakeEngine) ImportVolume(ctx context.Context, ref sandbox.VolumeReference, name string, r io.Reader, size int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return e.WriteVolume(ctx, ref, name, data)
}

func (e *fakeEngine) SnapshotVolume(_ context.Context, source sandbox.VolumeReference, key string, capacity int64) (sandbox.Volume, error) {
	if err := e.fail("snapshot:" + key); err != nil {
		return sandbox.Volume{}, err
	}
	if existing, ok := e.keys[key]; ok {
		return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: e.store, VolumeID: existing}, Capacity: capacity}, nil
	}
	e.next++
	id := fmt.Sprintf("vol_%d", e.next)
	e.keys[key] = id
	e.volumes[id] = map[string][]byte{}
	e.dirs[id] = map[string]bool{}
	for name, body := range e.volumes[source.VolumeID] {
		e.volumes[id][name] = append([]byte(nil), body...)
	}
	for name := range e.dirs[source.VolumeID] {
		e.dirs[id][name] = true
	}
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: e.store, VolumeID: id}, Capacity: capacity}, nil
}

func (e *fakeEngine) ListVolume(_ context.Context, reference sandbox.VolumeReference, directory string) ([]sandbox.VolumeEntry, error) {
	if e.volumes[reference.VolumeID] == nil {
		return nil, fmt.Errorf("unknown volume")
	}
	seen := map[string]sandbox.VolumeEntry{}
	collect := func(full string, entry sandbox.VolumeEntry) {
		parent := path.Dir(full)
		if parent == "." {
			parent = ""
		}
		if parent != directory {
			return
		}
		seen[entry.Name] = entry
	}
	for name, body := range e.volumes[reference.VolumeID] {
		collect(name, sandbox.VolumeEntry{Name: path.Base(name), Type: "file", Size: int64(len(body))})
	}
	for name := range e.dirs[reference.VolumeID] {
		collect(name, sandbox.VolumeEntry{Name: path.Base(name), Type: "directory"})
	}
	var result []sandbox.VolumeEntry
	for _, entry := range seen {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (e *fakeEngine) LstatVolume(_ context.Context, reference sandbox.VolumeReference, name string) (sandbox.VolumeEntry, error) {
	body, ok := e.volumes[reference.VolumeID][name]
	if !ok {
		return sandbox.VolumeEntry{}, fmt.Errorf("absent")
	}
	return sandbox.VolumeEntry{Name: path.Base(name), Type: "file", Size: int64(len(body))}, nil
}

func (e *fakeEngine) ReadVolume(_ context.Context, reference sandbox.VolumeReference, name string, offset, length int64) ([]byte, error) {
	body, ok := e.volumes[reference.VolumeID][name]
	if !ok {
		return nil, fmt.Errorf("absent %s", name)
	}
	if offset >= int64(len(body)) {
		return nil, nil
	}
	end := offset + length
	if end > int64(len(body)) {
		end = int64(len(body))
	}
	return body[offset:end], nil
}

// memoryRepository is the durable record only. It keeps no engine state, so a
// test can restart the publisher against the same rows the way a daemon would.
type memoryRepository struct {
	rows  map[string]*Publication
	calls map[string]int
	fail  map[string]error
}

func newRepository() *memoryRepository {
	return &memoryRepository{rows: map[string]*Publication{}, calls: map[string]int{}, fail: map[string]error{}}
}
func key(p Publication) string { return p.StoreID + "|" + p.Purpose + "|" + p.RevisionDigest }

func (r *memoryRepository) step(name string) error {
	r.calls[name]++
	if err, ok := r.fail[name]; ok {
		delete(r.fail, name)
		return err
	}
	return nil
}

func (r *memoryRepository) ClaimContentPublication(_ context.Context, p Publication) (Publication, error) {
	if err := r.step("claim"); err != nil {
		return Publication{}, err
	}
	if existing, ok := r.rows[key(p)]; ok {
		if existing.Capacity != p.Capacity || Digest(existing.Entries) != p.RevisionDigest {
			return Publication{}, fmt.Errorf("existing publication declares different content")
		}
		return *existing, nil
	}
	row := p
	row.Stage = "preparing"
	r.rows[key(p)] = &row
	return row, nil
}

func (r *memoryRepository) RecordContentStaging(_ context.Context, p Publication, volume string) (Publication, error) {
	if err := r.step("staging"); err != nil {
		return Publication{}, err
	}
	row := r.rows[key(p)]
	if row.StagingVolumeID != "" && row.StagingVolumeID != volume {
		return Publication{}, fmt.Errorf("already staged on a different volume")
	}
	row.StagingVolumeID, row.Stage = volume, "staged"
	return *row, nil
}

func (r *memoryRepository) RecordContentDerivation(_ context.Context, p Publication, volume string) (Publication, error) {
	if err := r.step("derivation"); err != nil {
		return Publication{}, err
	}
	row := r.rows[key(p)]
	if row.DerivedVolumeID != "" && row.DerivedVolumeID != volume {
		return Publication{}, fmt.Errorf("already derived a different volume")
	}
	row.DerivedVolumeID, row.Stage = volume, "derived"
	return *row, nil
}

func (r *memoryRepository) CommitContentPublication(_ context.Context, p Publication, volume, verified string) (Publication, error) {
	if err := r.step("commit"); err != nil {
		return Publication{}, err
	}
	row := r.rows[key(p)]
	if verified != row.RevisionDigest || row.DerivedVolumeID != volume {
		return Publication{}, fmt.Errorf("commit does not match verified derivation")
	}
	row.PublishedVolumeID, row.Stage = volume, "published"
	return *row, nil
}

func (r *memoryRepository) FailContentPublication(_ context.Context, p Publication, reason string) error {
	row, ok := r.rows[key(p)]
	if !ok {
		return fmt.Errorf("unknown publication")
	}
	if row.Stage == "published" {
		return fmt.Errorf("published content cannot fail")
	}
	row.Stage, row.Error = "failed", reason
	return nil
}

func (r *memoryRepository) FindPublishedContent(_ context.Context, store, purpose, revision string) (string, bool, error) {
	row, ok := r.rows[store+"|"+purpose+"|"+revision]
	if !ok || row.Stage != "published" {
		return "", false, nil
	}
	return row.PublishedVolumeID, true, nil
}

func fixture(t *testing.T) (Publisher, *fakeEngine, *memoryRepository, Revision) {
	t.Helper()
	engine, repository := newEngine(), newRepository()
	revision, err := Read(authored(t, map[string]string{"AGENTS.md": "shared context\n", "skills/one/SKILL.md": "step\n"}))
	if err != nil {
		t.Fatal(err)
	}
	return Publisher{Repository: repository, Backend: engine}, engine, repository, revision
}

func TestPublishVerifiesPublishedBytesAndIsIdempotent(t *testing.T) {
	publisher, engine, repository, revision := fixture(t)
	ctx := context.Background()
	volume, err := publisher.Publish(ctx, "assets", revision, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if volume == "" {
		t.Fatal("published volume identity missing")
	}
	row := repository.rows[engine.store+"|assets|"+revision.Digest]
	if row.Stage != "published" || row.PublishedVolumeID != volume || row.StagingVolumeID == volume {
		t.Fatalf("publication record: %+v", row)
	}
	if got := string(engine.volumes[volume]["AGENTS.md"]); got != "shared context\n" {
		t.Fatalf("published bytes: %q", got)
	}
	// A second publish of the same authored bytes must not touch the engine again.
	before := engine.next
	again, err := publisher.Publish(ctx, "assets", revision, 8<<20)
	if err != nil || again != volume {
		t.Fatalf("republish: %s %v", again, err)
	}
	if engine.next != before {
		t.Fatal("republish created new engine volumes")
	}
}

func TestPublishResumesAfterCrashAtEveryBoundary(t *testing.T) {
	boundaries := []string{"staging", "derivation", "commit"}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			publisher, engine, repository, revision := fixture(t)
			ctx := context.Background()
			repository.fail[boundary] = errors.New("interrupted before durable commit")
			if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); err == nil {
				t.Fatal("interrupted publication reported success")
			}
			if row := repository.rows[engine.store+"|assets|"+revision.Digest]; row.Stage == "published" {
				t.Fatal("interrupted publication committed a pointer")
			}
			volume, err := publisher.Publish(ctx, "assets", revision, 8<<20)
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			// Across every attempt the engine holds exactly one staging and one
			// published object, so an interrupted publication leaks neither.
			if engine.next != 2 {
				t.Fatalf("interrupted publication leaked engine volumes: %d", engine.next)
			}
			if got := string(engine.volumes[volume]["skills/one/SKILL.md"]); got != "step\n" {
				t.Fatalf("resumed content: %q", got)
			}
		})
	}
}

func TestPublishResumesAfterLostEngineReplies(t *testing.T) {
	for _, call := range []string{"create", "snapshot"} {
		t.Run(call, func(t *testing.T) {
			publisher, engine, _, revision := fixture(t)
			ctx := context.Background()
			// Fail the keyed call once; the retry must replay onto the same object.
			if call == "create" {
				engine.failCall["create:content-staging:assets:"+revision.Digest+":g0"] = errors.New("lost reply")
			} else {
				engine.failCall["snapshot:content-publish:assets:"+revision.Digest+":g0"] = errors.New("lost reply")
			}
			if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); err == nil {
				t.Fatalf("%s failure reported success", call)
			}
			volume, err := publisher.Publish(ctx, "assets", revision, 8<<20)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if engine.next > 2 {
				t.Fatalf("retry leaked engine volumes: %d", engine.next)
			}
			if got := string(engine.volumes[volume]["AGENTS.md"]); got != "shared context\n" {
				t.Fatalf("content after retry: %q", got)
			}
		})
	}
}

func TestPublishRefusesContradictoryContent(t *testing.T) {
	publisher, engine, repository, revision := fixture(t)
	ctx := context.Background()
	// The engine ends up holding bytes that disagree with the claimed revision.
	// Verification must refuse before any pointer is committed, and record a
	// terminal, diagnosable failure rather than retrying the contradiction.
	engine.corrupt["AGENTS.md"] = []byte("not the reviewed bytes\n")
	_, err := publisher.Publish(ctx, "assets", revision, 8<<20)
	if !errors.Is(err, ErrContentRefused) {
		t.Fatalf("contradictory content was not refused as terminal: %v", err)
	}
	row := repository.rows[engine.store+"|assets|"+revision.Digest]
	if row.Stage != "failed" || row.Error == "" {
		t.Fatalf("terminal refusal was not recorded: %+v", row)
	}
	if row.PublishedVolumeID != "" {
		t.Fatal("failed publication committed a published volume")
	}
	// A failed revision resolves to nothing a launch could mount.
	if volume, found, err := repository.FindPublishedContent(ctx, engine.store, "assets", revision.Digest); err != nil || found || volume != "" {
		t.Fatalf("failed revision resolved to a volume: %s %v %v", volume, found, err)
	}
}

// A durable write that does not land says nothing about content, so it must leave
// the claim resumable. Recording it terminal would strand a correct revision.
func TestPublishKeepsDurableWriteFailuresResumable(t *testing.T) {
	publisher, engine, repository, revision := fixture(t)
	ctx := context.Background()
	repository.fail["staging"] = errors.New("durable write did not land")
	if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); err == nil {
		t.Fatal("interrupted publication reported success")
	}
	if row := repository.rows[engine.store+"|assets|"+revision.Digest]; row.Stage == "failed" {
		t.Fatalf("a durable write failure was recorded as a content refusal: %+v", row)
	}
	if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); err != nil {
		t.Fatalf("resume after a durable write failure: %v", err)
	}
}

func TestPublishQuarantinesUncertainWritesWithoutRetryOrSnapshot(t *testing.T) {
	for _, boundary := range []string{"write", "population-commit"} {
		t.Run(boundary, func(t *testing.T) {
			publisher, engine, repository, revision := fixture(t)
			ctx := context.Background()
			if boundary == "write" {
				engine.failCall["write:AGENTS.md"] = &sandbox.Error{Class: sandbox.Unavailable, Operation: "volume import", Cause: errors.New("reply lost")}
			} else {
				repository.fail["populated"] = errors.New("durable finish reply lost")
			}
			if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); err == nil {
				t.Fatal("unknown outcome reported success")
			}
			writes := engine.calls["write:AGENTS.md"]
			if _, err := publisher.Publish(ctx, "assets", revision, 8<<20); !errors.Is(err, ErrPublicationQuarantined) {
				t.Fatal("unknown write replayed", err)
			}
			if engine.calls["write:AGENTS.md"] != writes || engine.next != 1 {
				t.Fatal("old writer was replayed or snapshotted")
			}
		})
	}
}

func TestPublishRejectsIncoherentRequests(t *testing.T) {
	publisher, _, _, revision := fixture(t)
	ctx := context.Background()
	if _, err := publisher.Publish(ctx, "knowledge", revision, 8<<20); err == nil {
		t.Fatal("unsupported purpose accepted")
	}
	if _, err := publisher.Publish(ctx, "assets", revision, 1); err == nil {
		t.Fatal("capacity below content size accepted")
	}
	tampered := revision
	tampered.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := publisher.Publish(ctx, "assets", tampered, 8<<20); err == nil {
		t.Fatal("digest disagreeing with entries accepted")
	}
	empty := revision
	empty.Entries = nil
	if _, err := publisher.Publish(ctx, "assets", empty, 8<<20); err == nil {
		t.Fatal("revision without entries accepted")
	}
	if _, err := (Publisher{}).Publish(ctx, "assets", revision, 8<<20); err == nil {
		t.Fatal("publisher without ports accepted")
	}
}

func TestBuiltinPublicationReplayChecksOriginalCapacity(t *testing.T) {
	publisher, _, _, revision := fixture(t)
	ctx := context.Background()
	first, err := publisher.Publish(ctx, "builtin", revision, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publisher.Publish(ctx, "builtin", revision, 8<<20)
	if err != nil || first != second {
		t.Fatal(first, second, err)
	}
	if _, err = publisher.Publish(ctx, "builtin", revision, 16<<20); err == nil {
		t.Fatal("published shortcut ignored changed physical capacity")
	}
}

func TestVerificationBoundsObservedBytesAcrossDirectories(t *testing.T) {
	engine := newEngine()
	volume := "untrusted-observation"
	engine.volumes[volume] = map[string][]byte{}
	engine.dirs[volume] = map[string]bool{}
	// Each individual file is permitted, but their complete observed tree is
	// larger than the reviewed content bound. Sharing bytes keeps the fixture
	// compact while verification still walks and hashes actual declared sizes.
	body := make([]byte, MaxFileBytes)
	for i := 0; i < 9; i++ {
		directory := fmt.Sprintf("directory-%d", i)
		engine.dirs[volume][directory] = true
		engine.volumes[volume][directory+"/file"] = body
	}
	err := VerifyVolume(context.Background(), engine, sandbox.VolumeReference{StoreID: engine.store, VolumeID: volume}, "sha256:"+strings.Repeat("a", 64), ReviewedProfile)
	if !errors.Is(err, ErrContentRefused) || !strings.Contains(err.Error(), "exceeds 67108864 bytes") {
		t.Fatal("observed tree escaped total verification bound", err)
	}
}

func TestPublishRefusesSourceChangedAfterRevision(t *testing.T) {
	engine, repository := newEngine(), newRepository()
	root := authored(t, map[string]string{"AGENTS.md": "original\n"})
	revision, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("rewritten-after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = (Publisher{Repository: repository, Backend: engine}).Publish(context.Background(), "assets", revision, 8<<20)
	if !errors.Is(err, ErrContentRefused) {
		t.Fatalf("source drift should be refused: %v", err)
	}
}

func TestAuthoredProfileStreamsBeyondReviewedLimits(t *testing.T) {
	publisher, engine, _, _ := fixture(t)
	root := authored(t, map[string]string{"large.bin": strings.Repeat("a", MaxFileBytes+1)})
	body, err := os.ReadFile(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{Path: "large.bin", Size: int64(len(body)), Digest: contentDigest(body)}}
	revision := Revision{Reader: directoryReader{root: root}, Profile: AuthoredProfile, Digest: Digest(entries), Entries: entries, TotalBytes: int64(len(body))}
	capacity, err := MaterializationCapacity(entries)
	if err != nil {
		t.Fatal(err)
	}
	volume, err := publisher.Publish(context.Background(), "authored", revision, capacity)
	if err != nil {
		t.Fatal(err)
	}
	ref := sandbox.VolumeReference{StoreID: engine.store, VolumeID: volume}
	if err = VerifyVolume(context.Background(), engine, ref, revision.Digest, AuthoredProfile); err != nil {
		t.Fatal(err)
	}
	if err = VerifyVolume(context.Background(), engine, ref, revision.Digest, ReviewedProfile); !errors.Is(err, ErrContentRefused) {
		t.Fatal("reviewed profile widened", err)
	}
	if _, err = publisher.Publish(context.Background(), "assets", revision, capacity); err == nil {
		t.Fatal("authored source granted system role")
	}
}

func (r *memoryRepository) StartContentPopulation(_ context.Context, p Publication) (Publication, error) {
	row := r.rows[key(p)]
	if row.Stage != "staged" {
		return Publication{}, ErrPublicationQuarantined
	}
	row.Stage = "populating"
	return *row, nil
}
func (r *memoryRepository) FinishContentPopulation(_ context.Context, p Publication) (Publication, error) {
	if err := r.step("populated"); err != nil {
		return Publication{}, err
	}
	row := r.rows[key(p)]
	if row.Stage != "populating" {
		return Publication{}, ErrPublicationQuarantined
	}
	row.Stage = "populated"
	return *row, nil
}
func (r *memoryRepository) QuarantineContentPublication(_ context.Context, p Publication) error {
	row := r.rows[key(p)]
	row.Stage = "quarantined"
	return nil
}

func TestPublisherRechecksPrincipalBeforeSnapshot(t *testing.T) {
	publisher, engine, repository, revision := fixture(t)
	checks := 0
	denied := errors.New("principal disabled")
	publisher.Authorize = func(context.Context) error {
		checks++
		if checks == 3 {
			return denied
		}
		return nil
	}
	if _, err := publisher.Publish(context.Background(), "assets", revision, 8<<20); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	row := repository.rows[engine.store+"|assets|"+revision.Digest]
	if row.Stage != "populated" || engine.next != 1 {
		t.Fatal("disabled principal sealed content", row.Stage, engine.next)
	}
	publisher.Authorize = nil
	if _, err := publisher.Publish(context.Background(), "assets", revision, 8<<20); err != nil {
		t.Fatal(err)
	}
	if engine.calls["write:AGENTS.md"] != 1 {
		t.Fatal("reauthorization rewrote settled source")
	}
}
