package product

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/previewmodel"
)

type PreviewJobStatus struct {
	ID       string                 `json:"id"`
	Digest   string                 `json:"digest"`
	State    string                 `json:"state"`
	Failure  string                 `json:"failure,omitempty"`
	Manifest *previewmodel.Manifest `json:"manifest,omitempty"`
}
type previewJob struct {
	PreviewJobStatus
	key, directory string
	db             *sql.DB
	refs           int
	cancel         context.CancelFunc
	ready          chan struct{}
}
type previewJobLease struct {
	scope Scope
	job   *previewJob
	timer *time.Timer
}

// PreviewJobs deduplicates immutable source/adapter work. Every browser receives
// its own lease; the final release terminates work and deletes derived artifacts.
type PreviewJobs struct {
	Files     *Files
	Worker    string
	Directory string
	mu        sync.Mutex
	jobs      map[string]*previewJob
	leases    map[string]*previewJobLease
	closed    bool
	group     sync.WaitGroup
	running   int
}

func (p *PreviewJobs) Open(ctx context.Context, scope Scope, snapshot, kind string) (PreviewJobStatus, error) {
	if kind != "docx" && kind != "xlsx" {
		return PreviewJobStatus{}, previewmodel.ErrUnsupported
	}
	descriptor, reader, release, err := p.Files.PreviewSnapshots().Acquire(ctx, scope, snapshot)
	if err != nil {
		return PreviewJobStatus{}, err
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	if p.Worker == "" || !filepath.IsAbs(p.Worker) {
		return PreviewJobStatus{}, previewmodel.ErrUnsupported
	}
	key := scope.Actor + "\x00" + scope.ProjectID + "\x00" + descriptor.Digest + "\x00" + kind + "\x00" + previewmodel.Version
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.leases) >= 64 {
		return PreviewJobStatus{}, ErrCapacity
	}
	if p.jobs == nil {
		p.jobs = map[string]*previewJob{}
		p.leases = map[string]*previewJobLease{}
	}
	job := p.jobs[key]
	if job == nil {
		if len(p.jobs) >= 16 || p.running >= 2 {
			return PreviewJobStatus{}, ErrCapacity
		}
		if err = os.MkdirAll(p.Directory, 0700); err != nil {
			return PreviewJobStatus{}, err
		}
		directory, e := os.MkdirTemp(p.Directory, "job-")
		if e != nil {
			return PreviewJobStatus{}, e
		}
		jobCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		job = &previewJob{PreviewJobStatus: PreviewJobStatus{Digest: descriptor.Digest, State: "indexing"}, key: key, directory: directory, cancel: cancel, ready: make(chan struct{})}
		p.jobs[key] = job
		p.running++
		transferred = true
		p.group.Add(1)
		go p.run(jobCtx, job, reader, release, kind)
	}
	job.refs++
	id := uuid.NewString()
	lease := &previewJobLease{scope: scope, job: job}
	p.leases[id] = lease
	lease.timer = time.AfterFunc(5*time.Minute, func() { p.expire(id) })
	status := job.PreviewJobStatus
	status.ID = id
	return status, nil
}
func (p *PreviewJobs) run(ctx context.Context, job *previewJob, reader io.Reader, release func(), kind string) {
	defer p.group.Done()
	failure := ""
	var db *sql.DB
	var manifest previewmodel.Manifest
	source, err := os.OpenFile(filepath.Join(job.directory, "source"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = io.Copy(source, reader)
		source.Close()
	}
	release()
	if err == nil {
		err = p.convert(ctx, job.directory, kind)
	}
	os.Remove(filepath.Join(job.directory, "source"))
	if err == nil {
		db, err = previewmodel.Open(job.directory)
	}
	if err == nil {
		manifest, err = previewmodel.ReadManifest(ctx, db)
	}
	if err != nil {
		failure = "renderer_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			failure = "parse_timeout"
		}
		if errors.Is(err, previewmodel.ErrInvalid) || errors.Is(err, previewmodel.ErrLimit) || errors.Is(err, previewmodel.ErrUnsupported) {
			failure = err.Error()
		}
		if db != nil {
			db.Close()
			db = nil
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running--
	close(job.ready)
	if job.refs == 0 || p.closed {
		if db != nil {
			db.Close()
		}
		os.RemoveAll(job.directory)
		delete(p.jobs, job.key)
		return
	}
	job.db = db
	job.Failure = failure
	if failure != "" {
		job.State = "failed"
		os.RemoveAll(job.directory)
	} else {
		job.State = "ready"
		job.Manifest = &manifest
	}
}
func (p *PreviewJobs) convert(ctx context.Context, directory, kind string) error {
	// The worker sees runtime libraries, its executable, one source and one output
	// directory. Network, host home/config, application state and Agent credentials
	// are absent. prlimit is a hard address-space/CPU/file-size bound; GOMEMLIMIT
	// additionally reduces Go's working set. tmpfs bounds parser scratch storage.
	args := []string{"--as=2147483648", "--cpu=30", "--fsize=134217728", "--nofile=128", "--", "bwrap", "--die-with-parent", "--unshare-all", "--new-session", "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib", "--proc", "/proc", "--dev", "/dev", "--size", "67108864", "--tmpfs", "/tmp", "--dir", "/work", "--bind", directory, "/work", "--ro-bind", filepath.Join(directory, "source"), "/source", "--ro-bind", p.Worker, "/worker", "--clearenv", "--setenv", "HOME", "/tmp", "--setenv", "TMPDIR", "/tmp", "--setenv", "GOMEMLIMIT", "268435456", "--chdir", "/work", "/worker", "/source", "/work", kind}
	command := exec.CommandContext(ctx, "prlimit", args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	var output boundedPreviewOutput
	command.Stdout = &output
	err := command.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		switch strings.TrimSpace(string(output)) {
		case "container_invalid":
			return previewmodel.ErrInvalid
		case "content_limit":
			return previewmodel.ErrLimit
		case "unsupported_feature":
			return previewmodel.ErrUnsupported
		}
		return err
	}
	return nil
}

type boundedPreviewOutput []byte

func (b *boundedPreviewOutput) Write(data []byte) (int, error) {
	n := len(data)
	if len(*b)+n > 256 {
		return 0, previewmodel.ErrLimit
	}
	*b = append(*b, data...)
	return n, nil
}
func (p *PreviewJobs) acquire(ctx context.Context, scope Scope, id string) (*previewJob, func(), error) {
	if err := p.Files.PreviewSnapshots().authorize(ctx, scope); err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	lease := p.leases[id]
	if lease == nil {
		return nil, nil, ErrSnapshotExpired
	}
	if lease.scope != scope {
		return nil, nil, ErrNotFound
	}
	job := lease.job
	lease.timer.Reset(5 * time.Minute)
	job.refs++
	var once sync.Once
	return job, func() { once.Do(func() { p.mu.Lock(); defer p.mu.Unlock(); p.drop(job) }) }, nil
}
func (p *PreviewJobs) Status(ctx context.Context, scope Scope, id string) (PreviewJobStatus, error) {
	job, release, err := p.acquire(ctx, scope, id)
	if err != nil {
		return PreviewJobStatus{}, err
	}
	defer release()
	p.mu.Lock()
	defer p.mu.Unlock()
	status := job.PreviewJobStatus
	status.ID = id
	return status, nil
}
func (p *PreviewJobs) Window(ctx context.Context, scope Scope, id string, sheet, start, limit int) (previewmodel.Window, error) {
	job, release, err := p.acquire(ctx, scope, id)
	if err != nil {
		return previewmodel.Window{}, err
	}
	defer release()
	p.mu.Lock()
	db := job.db
	p.mu.Unlock()
	if db == nil {
		return previewmodel.Window{}, p.jobError(job)
	}
	return previewmodel.ReadWindow(ctx, db, sheet, start, limit)
}
func (p *PreviewJobs) Find(ctx context.Context, scope Scope, id string, sheet, start int, query string) (int, error) {
	job, release, err := p.acquire(ctx, scope, id)
	if err != nil {
		return -1, err
	}
	defer release()
	p.mu.Lock()
	db := job.db
	p.mu.Unlock()
	if db == nil {
		return -1, p.jobError(job)
	}
	return previewmodel.Find(ctx, db, sheet, start, query)
}

func (p *PreviewJobs) jobError(job *previewJob) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch job.Failure {
	case "container_invalid":
		return previewmodel.ErrInvalid
	case "content_limit":
		return previewmodel.ErrLimit
	case "unsupported_feature":
		return previewmodel.ErrUnsupported
	case "":
		return ErrPending
	default:
		return errors.New("preview job failed")
	}
}
func (p *PreviewJobs) drop(job *previewJob) {
	job.refs--
	if job.refs != 0 {
		return
	}
	job.cancel()
	select {
	case <-job.ready:
		if job.db != nil {
			job.db.Close()
		}
		os.RemoveAll(job.directory)
		delete(p.jobs, job.key)
	default:
	}
}
func (p *PreviewJobs) expire(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if lease := p.leases[id]; lease != nil {
		delete(p.leases, id)
		lease.timer.Stop()
		p.drop(lease.job)
	}
}
func (p *PreviewJobs) Release(ctx context.Context, scope Scope, id string) error {
	if err := p.Files.PreviewSnapshots().authorize(ctx, scope); err != nil {
		return err
	}
	p.mu.Lock()
	lease := p.leases[id]
	if lease != nil && lease.scope != scope {
		p.mu.Unlock()
		return ErrNotFound
	}
	p.mu.Unlock()
	p.expire(id)
	return nil
}
func (p *PreviewJobs) Close() {
	p.mu.Lock()
	p.closed = true
	for id, lease := range p.leases {
		delete(p.leases, id)
		lease.timer.Stop()
		p.drop(lease.job)
	}
	p.mu.Unlock()
	p.group.Wait()
}
