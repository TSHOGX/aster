package authoring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"volume-studio/backend/internal/manifest"
)

var testActor = Actor{Issuer: "https://identity.example", Subject: "admin-subject"}

func testStore(t *testing.T) (*Store, Config) {
	t.Helper()
	c := Config{Directory: t.TempDir(), PublisherID: "publisher-fixture", QuotaBytes: 32 << 20}
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, c
}
func library(t *testing.T, s *Store) Library {
	t.Helper()
	l, err := s.Create(context.Background(), testActor, CreateInput{RequestID: "create", Name: "Library"})
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func mutate(t *testing.T, s *Store, id string, m Mutation) MutationResult {
	t.Helper()
	r, err := s.Mutate(context.Background(), testActor, id, m)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func publish(t *testing.T, s *Store, id string, rev int64) Version {
	t.Helper()
	v, err := s.Freeze(context.Background(), testActor, id, PublishInput{RequestID: "publish", ExpectedRevision: rev, Label: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func allow(context.Context, Actor) error { return nil }
func TestRevisionReceiptsFreezeRestoreAndRestart(t *testing.T) {
	s, c := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	m := Mutation{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "readme.txt", Text: "first"}
	r := mutate(t, s, l.ID, m)
	if r.Revision != 2 {
		t.Fatal(r)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "edit", ExpectedRevision: 2, Action: "text", Path: "readme.txt", Text: "second"})
	if replay := mutate(t, s, l.ID, m); replay.Revision != 2 {
		t.Fatal("receipt after stale revision lost")
	}
	m.Text = "different"
	if _, err := s.Mutate(ctx, testActor, l.ID, m); !errors.Is(err, ErrConflict) {
		t.Fatal("request body rebound", err)
	}
	v := publish(t, s, l.ID, 3)
	mutate(t, s, l.ID, Mutation{RequestID: "later", ExpectedRevision: 3, Action: "text", Path: "readme.txt", Text: "later"})
	if _, err := s.Descriptor(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("pending source exported", err)
	}
	if worked, err := s.ValidateNext(ctx, allow); err != nil || !worked {
		t.Fatal(worked, err)
	}
	read, entry, err := s.Content(ctx, l.ID, "readme.txt", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(read)
	read.Close()
	if err != nil || string(data) != "second" {
		t.Fatal("freeze changed", string(data), err)
	}
	if entry.Digest != digestBytes([]byte("second")) {
		t.Fatal(entry)
	}
	diff, _, err := s.Diff(ctx, l.ID, "", 4)
	if err != nil || len(diff) != 1 || diff[0].Kind != "changed" {
		t.Fatal(diff, err)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "restore", ExpectedRevision: 4, Action: "restore", VersionID: v.ID})
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.Tree(ctx, l.ID, "", "", 5)
	if err != nil || page.Revision != 5 || page.Entries[0].Digest != entry.Digest {
		t.Fatal(page, err)
	}
	if got := mutate(t, s, l.ID, Mutation{RequestID: "restore", ExpectedRevision: 4, Action: "restore", VersionID: v.ID}); got.Revision != 5 {
		t.Fatal("restore receipt lost")
	}
	raw, err := s.Descriptor(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := manifest.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := d.Digest()
	if digest != v.DescriptorDigest {
		t.Fatal("descriptor identity changed")
	}
	s.Close()
	c.PublisherID = "different"
	if bad, err := Open(ctx, c); err == nil {
		bad.Close()
		t.Fatal("publisher rebound after restore")
	}
}
func TestTwoTabsCannotOverwriteAndParentsRemainValid(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	ctx := context.Background()
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Mutate(ctx, testActor, l.ID, Mutation{RequestID: fmt.Sprintf("tab-%d", i), ExpectedRevision: 1, Action: "text", Path: "file", Text: fmt.Sprint(i)})
			outcomes <- err
		}(i)
	}
	wg.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	for _, m := range []Mutation{{Action: "text", Path: "missing/file"}, {Action: "text", Path: "file/child"}, {Action: "mkdir", Path: " bad "}, {Action: "text", Path: "../escape"}} {
		m.RequestID = randomID()
		m.ExpectedRevision = 2
		if _, err := s.Mutate(ctx, testActor, l.ID, m); err == nil {
			t.Fatal("invalid tree accepted", m.Action, m.Path)
		}
	}
	mutate(t, s, l.ID, Mutation{RequestID: "mkdir", ExpectedRevision: 2, Action: "mkdir", Path: "目录"})
	mutate(t, s, l.ID, Mutation{RequestID: "child", ExpectedRevision: 3, Action: "text", Path: "目录/child", Text: "text"})
	page, err := s.Tree(ctx, l.ID, "目录", "", 4)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Path != "目录/child" {
		t.Fatal(page, err)
	}
	if _, err := s.Mutate(ctx, testActor, l.ID, Mutation{RequestID: "delete", ExpectedRevision: 4, Action: "remove", Path: "目录"}); !errors.Is(err, ErrConflict) {
		t.Fatal("recursive delete unconfirmed", err)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "rename", ExpectedRevision: 4, Action: "move", Path: "目录", To: "renamed"})
	if page, err := s.Tree(ctx, l.ID, "renamed", "", 5); err != nil || page.Entries[0].Path != "renamed/child" {
		t.Fatal(page, err)
	}
	removed := mutate(t, s, l.ID, Mutation{RequestID: "delete", ExpectedRevision: 5, Action: "remove", Path: "renamed", Recursive: true})
	if removed.Affected != 2 {
		t.Fatal(removed)
	}
}
func TestBlobGCHonorsReaderAndFrozenVersionPins(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	mutate(t, s, l.ID, Mutation{RequestID: "one", ExpectedRevision: 1, Action: "text", Path: "one", Text: "retained"})
	reader, entry, err := s.Content(ctx, l.ID, "one", "")
	if err != nil {
		t.Fatal(err)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "remove", ExpectedRevision: 2, Action: "remove", Path: "one"})
	if n, err := s.Collect(ctx); err != nil || n != 0 {
		t.Fatal("reader collected", n, err)
	}
	reader.Close()
	reader.Close()
	if n, err := s.Collect(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = os.Stat(s.blobPath(entry.Digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreferenced file retained", err)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "again", ExpectedRevision: 3, Action: "text", Path: "one", Text: "retained"})
	publish(t, s, l.ID, 4)
	mutate(t, s, l.ID, Mutation{RequestID: "clear", ExpectedRevision: 4, Action: "remove", Path: "one"})
	if n, err := s.Collect(ctx); err != nil || n != 0 {
		t.Fatal("pending validation pin collected", n, err)
	}
	if _, err = s.ValidateNext(ctx, allow); err != nil {
		t.Fatal(err)
	}
}
func TestDurableOrphansAndMissingReferencedBlobRecovery(t *testing.T) {
	s, c := testStore(t)
	l := library(t, s)
	ctx := context.Background()
	mutate(t, s, l.ID, Mutation{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "one", Text: "source"})
	s.Close()
	orphan := []byte("orphan")
	digest := digestBytes(orphan)
	if err := os.WriteFile(filepath.Join(c.Directory, "blobs", digest[7:]), orphan, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.usage(ctx); err != nil || n != int64(len(orphan)+len("source")) {
		t.Fatal("orphan unaccounted", n, err)
	}
	if n, err := s.Collect(ctx); err != nil || n != 1 {
		t.Fatal("orphan not collected", n, err)
	}
	s.Close()
	if err = os.Remove(filepath.Join(c.Directory, "blobs", digestBytes([]byte("source"))[7:])); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, c); err == nil {
		reopened.Close()
		t.Fatal("missing referenced blob accepted")
	}
}
func TestPublicationChecksPrincipalAtFinalization(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	ctx := context.Background()
	mutate(t, s, l.ID, Mutation{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "file", Text: "source"})
	v := publish(t, s, l.ID, 2)
	checks := 0
	denied := errors.New("principal revoked")
	if _, err := s.ValidateNext(ctx, func(context.Context, Actor) error {
		checks++
		if checks == 2 {
			return denied
		}
		return nil
	}); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	current, err := s.Version(ctx, v.ID)
	if err != nil || current.State == "published" {
		t.Fatal("revoked publication finalized", current, err)
	}
	if _, err = s.ValidateNext(ctx, allow); err != nil {
		t.Fatal(err)
	}
	current, err = s.Version(ctx, v.ID)
	if err != nil || current.State != "published" {
		t.Fatal(current, err)
	}
}
func TestQuotaPreservesExistingDraftAndEmptyTreeRejected(t *testing.T) {
	s, _ := testStore(t)
	s.config.QuotaBytes = 1 << 20
	l := library(t, s)
	ctx := context.Background()
	if _, err := s.Freeze(ctx, testActor, l.ID, PublishInput{RequestID: "empty", ExpectedRevision: 1, Label: "empty"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty publication", err)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "file", Text: "kept"})
	if _, err := s.Mutate(ctx, testActor, l.ID, Mutation{RequestID: "large", ExpectedRevision: 2, Action: "text", Path: "file", Text: strings.Repeat("a", MaxTextBytes)}); !errors.Is(err, ErrQuota) {
		t.Fatal("quota", err)
	}
	f, _, err := s.Content(ctx, l.ID, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "kept" {
		t.Fatal("replacement damaged prior draft")
	}
}

func TestCanceledValidationCannotFinalizeReplacementGeneration(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	mutate(t, s, l.ID, Mutation{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "file", Text: "source"})
	v := publish(t, s, l.ID, 2)
	atFinal := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		calls := 0
		_, err := s.ValidateNext(ctx, func(context.Context, Actor) error {
			calls++
			if calls == 2 {
				close(atFinal)
				<-release
			}
			return nil
		})
		done <- err
	}()
	<-atFinal
	if _, err := s.Operation(ctx, testActor, v.ID, OperationInput{RequestID: "cancel", Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	replacement := Actor{Issuer: testActor.Issuer, Subject: "replacement-admin"}
	if _, err := s.Operation(ctx, replacement, v.ID, OperationInput{RequestID: "retry", Action: "retry"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrConflict) {
		t.Fatal("old validation finalized", err)
	}
	current, err := s.Version(ctx, v.ID)
	if err != nil || current.State != "pending" || current.Publisher != replacement {
		t.Fatal(current, err)
	}
	if _, err = s.ValidateNext(ctx, func(ctx context.Context, a Actor) error {
		if a != replacement {
			t.Fatal("wrong worker principal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRevokedPublisherPausesWithoutStarvingOtherLibrary(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	first := library(t, s)
	mutate(t, s, first.ID, Mutation{RequestID: "one", ExpectedRevision: 1, Action: "text", Path: "file", Text: "one"})
	v := publish(t, s, first.ID, 2)
	other := Actor{Issuer: testActor.Issuer, Subject: "other-admin"}
	second, err := s.Create(ctx, other, CreateInput{RequestID: "other-library", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(ctx, other, second.ID, Mutation{RequestID: "two", ExpectedRevision: 1, Action: "text", Path: "file", Text: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Freeze(ctx, other, second.ID, PublishInput{RequestID: "publish", ExpectedRevision: 2, Label: "v1"}); err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, a Actor) error {
		if a == testActor {
			return ErrAuthority
		}
		return nil
	}
	for n := 0; n < 2; n++ {
		_, err = s.ValidateNext(ctx, check)
		if err != nil && !errors.Is(err, ErrAuthority) {
			t.Fatal(err)
		}
	}
	firstVersion, err := s.Version(ctx, v.ID)
	if err != nil || firstVersion.Error != "principal_authority_required" {
		t.Fatal(firstVersion, err)
	}
	others, err := s.Versions(ctx, second.ID, "")
	if err != nil || others[0].State != "published" {
		t.Fatal("revoked first job blocked other authors", others, err)
	}
	if _, err = s.Operation(ctx, other, v.ID, OperationInput{RequestID: "adopt", Action: "retry"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx, check); err != nil {
		t.Fatal(err)
	}
}

func TestSourceContentReaderCannotBeCollectedByRepeatedClose(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	ctx := context.Background()
	mutate(t, s, l.ID, Mutation{RequestID: "file", ExpectedRevision: 1, Action: "text", Path: "file", Text: "same"})
	first, _, err := s.Content(ctx, l.ID, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Content(ctx, l.ID, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	mutate(t, s, l.ID, Mutation{RequestID: "remove", ExpectedRevision: 2, Action: "remove", Path: "file"})
	first.Close()
	first.Close()
	if n, err := s.Collect(ctx); err != nil || n != 0 {
		t.Fatal("double close released another reader", n, err)
	}
	second.Close()
	if n, err := s.Collect(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
