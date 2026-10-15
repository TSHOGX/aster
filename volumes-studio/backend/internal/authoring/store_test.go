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

	"aster/contracts/evolution"
	"aster/contracts/volumemanifest"
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
func TestBranchReceiptsReleaseRestoreAndRestart(t *testing.T) {
	s, c := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	m := draftEdit{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "readme.txt", Text: "first"}
	r := editDraft(t, s, b, m)
	if r.Revision != 2 {
		t.Fatal(r)
	}
	editDraft(t, s, b, draftEdit{RequestID: "edit", ExpectedRevision: 2, Action: "text", Path: "readme.txt", Text: "second"})
	if replay := editDraft(t, s, b, m); replay.Revision != 2 {
		t.Fatal("receipt after stale revision lost")
	}
	m.Text = "different"
	if _, err := editDraftAs(s, ctx, testActor, b, m); !errors.Is(err, ErrConflict) {
		t.Fatal("request body rebound", err)
	}
	v := publishDraft(t, s, b, 3)
	editDraft(t, s, b, draftEdit{RequestID: "later", ExpectedRevision: 3, Action: "text", Path: "readme.txt", Text: "later"})
	if _, err := s.Descriptor(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("pending source exported", err)
	}
	if worked, err := s.ValidateNext(ctx); err != nil || !worked {
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
	current, err := s.Branch(ctx, testActor, l.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := s.BranchDiff(ctx, testActor, l.ID, b.ID, current.Head, "", 4)
	if err != nil || len(diff.Changes) != 1 || diff.Changes[0].Kind != "changed" {
		t.Fatal(diff, err)
	}
	restore := CreateBranchInput{"restore", "Restored release", v.AcceptedCommit}
	restored, err := s.CreateBranch(ctx, testActor, l.ID, restore)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	c.Authority = &branchAuthority{true}
	s, err = Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.BranchTree(ctx, testActor, l.ID, restored.ID, "", "", 1)
	if err != nil || page.Revision != 1 || page.Entries[0].Digest != entry.Digest {
		t.Fatal(page, err)
	}
	if got, err := s.CreateBranch(ctx, testActor, l.ID, restore); err != nil || got.ID != restored.ID {
		t.Fatal("restore branch receipt lost", got, err)
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
	b := newDraftBranch(t, s, l.ID)
	ctx := context.Background()
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := editDraftAs(s, ctx, testActor, b, draftEdit{RequestID: fmt.Sprintf("tab-%d", i), ExpectedRevision: 1, Action: "text", Path: "file", Text: fmt.Sprint(i)})
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
	for _, m := range []draftEdit{{Action: "text", Path: "missing/file"}, {Action: "text", Path: "file/child"}, {Action: "mkdir", Path: " bad "}, {Action: "text", Path: "../escape"}} {
		m.RequestID = randomID()
		m.ExpectedRevision = 2
		if _, err := editDraftAs(s, ctx, testActor, b, m); err == nil {
			t.Fatal("invalid tree accepted", m.Action, m.Path)
		}
	}
	editDraft(t, s, b, draftEdit{RequestID: "mkdir", ExpectedRevision: 2, Action: "mkdir", Path: "目录"})
	editDraft(t, s, b, draftEdit{RequestID: "child", ExpectedRevision: 3, Action: "text", Path: "目录/child", Text: "text"})
	page, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "目录", "", 4)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Path != "目录/child" {
		t.Fatal(page, err)
	}
	if _, err := editDraftAs(s, ctx, testActor, b, draftEdit{RequestID: "delete", ExpectedRevision: 4, Action: "remove", Path: "目录"}); !errors.Is(err, ErrConflict) {
		t.Fatal("recursive delete unconfirmed", err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "rename", ExpectedRevision: 4, Action: "move", Path: "目录", To: "renamed"})
	if page, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "renamed", "", 5); err != nil || page.Entries[0].Path != "renamed/child" {
		t.Fatal(page, err)
	}
	removed := editDraft(t, s, b, draftEdit{RequestID: "delete", ExpectedRevision: 5, Action: "remove", Path: "renamed", Recursive: true})
	if removed.Affected != 2 {
		t.Fatal(removed)
	}
}
func TestBlobGCHonorsReaderAndFrozenVersionPins(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	editDraft(t, s, b, draftEdit{RequestID: "one", ExpectedRevision: 1, Action: "text", Path: "one", Text: "retained"})
	reader, entry, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "one", 2)
	if err != nil {
		t.Fatal(err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "remove", ExpectedRevision: 2, Action: "remove", Path: "one"})
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
	editDraft(t, s, b, draftEdit{RequestID: "again", ExpectedRevision: 3, Action: "text", Path: "one", Text: "retained"})
	publishDraft(t, s, b, 4)
	editDraft(t, s, b, draftEdit{RequestID: "clear", ExpectedRevision: 4, Action: "remove", Path: "one"})
	if n, err := s.Collect(ctx); err != nil || n != 0 {
		t.Fatal("pending validation pin collected", n, err)
	}
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestDurableOrphansAndMissingReferencedBlobRecovery(t *testing.T) {
	s, c := testStore(t)
	l := library(t, s)
	ctx := context.Background()
	s.config.Authority = &branchAuthority{true}
	c.Authority = s.config.Authority
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"orphan-branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, branch.ID, SaveBranchTextInput{"save", 1, main.CommitID, "one", "source"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	orphan := []byte("orphan")
	digest := digestBytes(orphan)
	if err := os.WriteFile(filepath.Join(c.Directory, "blobs", digest[7:]), orphan, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, c)
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
	b := newDraftBranch(t, s, l.ID)
	ctx := context.Background()
	editDraft(t, s, b, draftEdit{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "file", Text: "source"})
	v := publishDraft(t, s, b, 2)
	calls := 0
	s.config.Authority = authorityFunc(func(context.Context, evolution.AccessRequest) (evolution.AccessDecision, error) {
		calls++
		return evolution.AccessDecision{Allowed: calls != 2, PolicyRevision: 1}, nil
	})
	if _, err := s.ValidateNext(ctx); !errors.Is(err, ErrAuthority) {
		t.Fatal("revoked final authority accepted", err)
	}
	current, err := s.Version(ctx, v.ID)
	if err != nil || current.State == "published" || calls != 2 {
		t.Fatal(current, err, calls)
	}
	s.config.Authority = &branchAuthority{true}
	if _, err = s.Operation(ctx, testActor, v.ID, OperationInput{RequestID: "retry-authority", Action: "retry"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx); err != nil {
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
	b := newDraftBranch(t, s, l.ID)
	ctx := context.Background()
	if _, err := releaseDraft(s, ctx, testActor, b, draftRelease{RequestID: "empty", ExpectedRevision: 1, Label: "empty"}); err == nil {
		t.Fatal("empty publication", err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "file", Text: "kept"})
	if _, err := editDraftAs(s, ctx, testActor, b, draftEdit{RequestID: "large", ExpectedRevision: 2, Action: "text", Path: "file", Text: strings.Repeat("a", MaxTextBytes)}); !errors.Is(err, ErrQuota) {
		t.Fatal("quota", err)
	}
	f, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "file", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	if string(data) != "kept" {
		t.Fatal("replacement damaged prior draft")
	}
}

func TestCanceledValidationCannotFinalizeReplacementGeneration(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	editDraft(t, s, b, draftEdit{RequestID: "save", ExpectedRevision: 1, Action: "text", Path: "file", Text: "source"})
	v := publishDraft(t, s, b, 2)
	atFinal := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	calls := 0
	s.config.Authority = authorityFunc(func(_ context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
		if request.Action == "volume.publish" {
			calls++
			if calls == 2 {
				close(atFinal)
				<-release
			}
		}
		return evolution.AccessDecision{Allowed: true, PolicyRevision: 1}, nil
	})
	go func() { _, err := s.ValidateNext(ctx); done <- err }()
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
	s.config.Authority = authorityFunc(func(_ context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
		if request.Principal.Subject != replacement.Subject || request.Principal.Issuer != replacement.Issuer {
			t.Fatal("wrong worker principal")
		}
		return evolution.AccessDecision{Allowed: true, PolicyRevision: 1}, nil
	})
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRevokedPublisherPausesWithoutStarvingOtherLibrary(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	first := library(t, s)
	b := newDraftBranch(t, s, first.ID)
	editDraft(t, s, b, draftEdit{RequestID: "one", ExpectedRevision: 1, Action: "text", Path: "file", Text: "one"})
	v := publishDraft(t, s, b, 2)
	other := Actor{Issuer: testActor.Issuer, Subject: "other-admin"}
	second, err := s.Create(ctx, other, CreateInput{RequestID: "other-library", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	b2 := newDraftBranch(t, s, second.ID)
	if _, err = editDraftAs(s, ctx, other, b2, draftEdit{RequestID: "two", ExpectedRevision: 1, Action: "text", Path: "file", Text: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err = releaseDraft(s, ctx, other, b2, draftRelease{RequestID: "publish", ExpectedRevision: 2, Label: "v1"}); err != nil {
		t.Fatal(err)
	}
	s.config.Authority = authorityFunc(func(_ context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
		return evolution.AccessDecision{Allowed: request.Principal.Subject != testActor.Subject, PolicyRevision: 1}, nil
	})
	for n := 0; n < 2; n++ {
		_, err = s.ValidateNext(ctx)
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
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSourceContentReaderCannotBeCollectedByRepeatedClose(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	ctx := context.Background()
	editDraft(t, s, b, draftEdit{RequestID: "file", ExpectedRevision: 1, Action: "text", Path: "file", Text: "same"})
	first, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "file", 2)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "file", 2)
	if err != nil {
		t.Fatal(err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "remove", ExpectedRevision: 2, Action: "remove", Path: "file"})
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
