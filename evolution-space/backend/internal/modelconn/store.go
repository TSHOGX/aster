// Package modelconn stores administrator-managed connections to OpenAI-compatible
// chat services and adapts them to the native model protocol. Keys are encrypted
// with a host secret that is never stored beside them.
package modelconn

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound  = errors.New("model connection not found")
	ErrConflict  = errors.New("model connection changed")
	ErrInvalid   = errors.New("invalid model connection")
	ErrNoKey     = errors.New("model connection key not configured")
	ErrNoSealKey = errors.New("model key encryption is not configured")
)

// Settings define what the model produces; changing any of them publishes a
// new pinned revision so existing workflows keep their exact model behavior.
type Settings struct {
	BaseURL         string   `json:"base_url"`
	Model           string   `json:"model"`
	MaxOutputTokens int64    `json:"max_output_tokens"`
	Temperature     *float64 `json:"temperature,omitempty"`
	JSONMode        bool     `json:"json_mode"`
}

// Policy bounds spend and time. It applies to every revision of a connection
// and can change without re-pinning workflows.
type Policy struct {
	CallTokens             int64 `json:"call_tokens"`
	InputMicrosPerMillion  int64 `json:"input_micros_per_million"`
	OutputMicrosPerMillion int64 `json:"output_micros_per_million"`
	TimeoutSeconds         int64 `json:"timeout_seconds"`
	Concurrency            int64 `json:"concurrency"`
}

type Connection struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Enabled   bool               `json:"enabled"`
	Settings  Settings           `json:"settings"`
	Policy    Policy             `json:"policy"`
	Current   evolution.Revision `json:"current"`
	KeySet    bool               `json:"key_set"`
	KeyHint   string             `json:"key_hint,omitempty"`
	UpdatedAt time.Time          `json:"updated_at"`
}

type Revision struct {
	Ref      evolution.Revision
	Settings Settings
}

const schema = `CREATE TABLE connections(id TEXT PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN(0,1)),policy BLOB NOT NULL,current_revision INTEGER NOT NULL CHECK(current_revision>0),key_ciphertext BLOB,key_hint TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL);
CREATE TABLE connection_revisions(id TEXT NOT NULL REFERENCES connections(id),revision INTEGER NOT NULL CHECK(revision>0),digest TEXT NOT NULL,settings BLOB NOT NULL,PRIMARY KEY(id,revision));
CREATE TRIGGER immutable_connection_revision BEFORE UPDATE ON connection_revisions BEGIN SELECT RAISE(ABORT,'immutable connection revision'); END;
CREATE TRIGGER retained_connection_revision BEFORE DELETE ON connection_revisions BEGIN SELECT RAISE(ABORT,'retain connection revision'); END;
CREATE TABLE calls(run_id TEXT NOT NULL,call_id TEXT NOT NULL,idempotency_key TEXT NOT NULL,request_digest TEXT NOT NULL,model TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN('running','completed','failed','cancelled')),instance TEXT NOT NULL,ceiling_tokens INTEGER NOT NULL,ceiling_cost INTEGER NOT NULL,observation BLOB,created_at INTEGER NOT NULL,PRIMARY KEY(run_id,call_id));
CREATE TABLE events(sequence INTEGER PRIMARY KEY AUTOINCREMENT,at INTEGER NOT NULL,actor TEXT NOT NULL,action TEXT NOT NULL,connection_id TEXT NOT NULL,revision INTEGER NOT NULL);
PRAGMA user_version=1;`

type Store struct {
	db       *sql.DB
	lock     *os.File
	path     string
	aead     cipher.AEAD
	instance string
	mu       sync.Mutex
}

// Open owns modelconn.db in the Evolution Space state directory. sealSecret
// may be empty; connections then remain listable but cannot store or use keys.
func Open(ctx context.Context, path, sealSecret string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute model connection database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("model connection store already owned: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: path}
	uri.RawQuery = url.Values{"_pragma": {"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)", "foreign_keys(1)"}}.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock, path: path, instance: randomHex(16)}
	if err = s.initialize(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		s.Close()
		return nil, err
	}
	if sealSecret != "" {
		if len(sealSecret) < 32 {
			s.Close()
			return nil, fmt.Errorf("model key encryption secret must have at least 32 characters")
		}
		key := sha256.Sum256([]byte("aster-model-connection-v1\x00" + sealSecret))
		block, _ := aes.NewCipher(key[:])
		s.aead, _ = cipher.NewGCM(block)
	}
	return s, nil
}
func (s *Store) Close() error {
	err := s.db.Close()
	s.lock.Close()
	return err
}
func (s *Store) initialize(ctx context.Context) error {
	var version, count int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		return err
	}
	if version == 0 && count == 0 {
		_, err := s.db.ExecContext(ctx, schema)
		return err
	}
	if version != 1 {
		return fmt.Errorf("unsupported model connection schema")
	}
	return nil
}

// Backup writes a consistent copy for the Evolution Space backup set. Keys stay
// encrypted; restoring them requires the same host secret.
func (s *Store) Backup(ctx context.Context, target string) error {
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", target)
	if err == nil {
		err = os.Chmod(target, 0600)
	}
	return err
}

func (s *Store) SealConfigured() bool { return s.aead != nil }

func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return fmt.Sprintf("%x", raw)
}

// ValidBaseURL accepts HTTPS service roots, or plain HTTP only on loopback for
// local adapters. Credentials, queries and fragments are never part of a URL.
func ValidBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(raw) > 512 {
		return fmt.Errorf("%w: base URL must be an absolute service address", ErrInvalid)
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && (ip != nil && ip.IsLoopback() || u.Hostname() == "localhost") {
		return nil
	}
	return fmt.Errorf("%w: base URL must use HTTPS", ErrInvalid)
}

// Normalize trims and validates settings exactly as Save does.
func (v Settings) Normalize() (Settings, error) {
	v.BaseURL = strings.TrimRight(strings.TrimSpace(v.BaseURL), "/")
	v.Model = strings.TrimSpace(v.Model)
	if err := ValidBaseURL(v.BaseURL); err != nil {
		return v, err
	}
	if v.Model == "" || len(v.Model) > 200 || strings.ContainsAny(v.Model, "\r\n") {
		return v, fmt.Errorf("%w: model name required", ErrInvalid)
	}
	if v.MaxOutputTokens < 1 || v.MaxOutputTokens > 1_000_000 {
		return v, fmt.Errorf("%w: maximum output tokens must be between 1 and 1000000", ErrInvalid)
	}
	if v.Temperature != nil && (*v.Temperature < 0 || *v.Temperature > 2) {
		return v, fmt.Errorf("%w: temperature must be between 0 and 2", ErrInvalid)
	}
	return v, nil
}
func (p Policy) valid(settings Settings) error {
	if p.CallTokens < settings.MaxOutputTokens+256 || p.CallTokens > 10_000_000 {
		return fmt.Errorf("%w: call token ceiling must exceed maximum output tokens by at least 256", ErrInvalid)
	}
	if p.InputMicrosPerMillion < 0 || p.OutputMicrosPerMillion < 0 || p.InputMicrosPerMillion > 1_000_000_000_000 || p.OutputMicrosPerMillion > 1_000_000_000_000 {
		return fmt.Errorf("%w: prices must be non-negative", ErrInvalid)
	}
	if p.TimeoutSeconds < 10 || p.TimeoutSeconds > 3600 || p.Concurrency < 1 || p.Concurrency > 64 {
		return fmt.Errorf("%w: timeout must be 10-3600 seconds and concurrency 1-64", ErrInvalid)
	}
	return nil
}

// CostCeiling is the most one call can cost at the ceiling token count.
func (p Policy) CostCeiling() int64 {
	price := p.InputMicrosPerMillion
	if p.OutputMicrosPerMillion > price {
		price = p.OutputMicrosPerMillion
	}
	return (p.CallTokens*price + 999_999) / 1_000_000
}

func digestFor(id string, revision int64, settings Settings) string {
	raw, _ := json.Marshal(struct {
		Protocol string   `json:"protocol"`
		ID       string   `json:"id"`
		Revision int64    `json:"revision"`
		Settings Settings `json:"settings"`
	}{"openai-chat-completions-v1", id, revision, settings})
	return evolution.Digest(raw)
}

// Input creates or updates a connection. A nil Key leaves the stored key
// unchanged; an empty non-nil Key is invalid.
type Input struct {
	ExpectedRevision int64    `json:"expected_revision"`
	Name             string   `json:"name"`
	Enabled          bool     `json:"enabled"`
	Settings         Settings `json:"settings"`
	Policy           Policy   `json:"policy"`
	Key              *string  `json:"api_key,omitempty"`
}

func (s *Store) Save(ctx context.Context, actor, id string, input Input) (Connection, error) {
	if !manifest.ValidID(id) || len(id) > 64 {
		return Connection{}, fmt.Errorf("%w: connection ID must use letters, digits, hyphens or underscores", ErrInvalid)
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		return Connection{}, fmt.Errorf("%w: name required", ErrInvalid)
	}
	settings, err := input.Settings.Normalize()
	if err != nil {
		return Connection{}, err
	}
	if err = input.Policy.valid(settings); err != nil {
		return Connection{}, err
	}
	var sealed []byte
	var hint string
	if input.Key != nil {
		key := strings.TrimSpace(*input.Key)
		if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
			return Connection{}, fmt.Errorf("%w: API key must be a single line", ErrInvalid)
		}
		if s.aead == nil {
			return Connection{}, ErrNoSealKey
		}
		nonce := make([]byte, s.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return Connection{}, err
		}
		sealed = s.aead.Seal(nonce, nonce, []byte(key), []byte(id))
		if len(key) > 8 {
			hint = "…" + key[len(key)-4:]
		}
	}
	policy, _ := json.Marshal(input.Policy)
	raw, _ := json.Marshal(settings)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Connection{}, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT current_revision FROM connections WHERE id=?", id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		return Connection{}, err
	}
	if current != input.ExpectedRevision {
		return Connection{}, ErrConflict
	}
	revision := current
	if current == 0 {
		revision = 1
		if input.Key == nil {
			return Connection{}, fmt.Errorf("%w: API key required for a new connection", ErrInvalid)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO connections(id,name,enabled,policy,current_revision,key_ciphertext,key_hint,updated_at) VALUES(?,?,?,?,1,?,?,?)", id, input.Name, input.Enabled, policy, sealed, hint, time.Now().UnixMilli()); err != nil {
			return Connection{}, err
		}
	} else {
		var prior []byte
		if err = tx.QueryRowContext(ctx, "SELECT settings FROM connection_revisions WHERE id=? AND revision=?", id, current).Scan(&prior); err != nil {
			return Connection{}, err
		}
		if string(prior) != string(raw) {
			revision = current + 1
		}
		if _, err = tx.ExecContext(ctx, "UPDATE connections SET name=?,enabled=?,policy=?,current_revision=?,updated_at=? WHERE id=?", input.Name, input.Enabled, policy, revision, time.Now().UnixMilli(), id); err != nil {
			return Connection{}, err
		}
		if input.Key != nil {
			if _, err = tx.ExecContext(ctx, "UPDATE connections SET key_ciphertext=?,key_hint=? WHERE id=?", sealed, hint, id); err != nil {
				return Connection{}, err
			}
		}
	}
	if revision != current {
		if _, err = tx.ExecContext(ctx, "INSERT INTO connection_revisions(id,revision,digest,settings) VALUES(?,?,?,?)", id, revision, digestFor(id, revision, settings), raw); err != nil {
			return Connection{}, err
		}
	}
	action := "update"
	if current == 0 {
		action = "create"
	} else if input.Key != nil {
		action = "update_with_key"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO events(at,actor,action,connection_id,revision) VALUES(?,?,?,?,?)", time.Now().UnixMilli(), actor, action, id, revision); err != nil {
		return Connection{}, err
	}
	if err = tx.Commit(); err != nil {
		return Connection{}, err
	}
	return s.connection(ctx, id)
}

func (s *Store) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM connections ORDER BY id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	result := []Connection{}
	for _, id := range ids {
		c, err := s.connection(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}
func (s *Store) Get(ctx context.Context, id string) (Connection, error) { return s.connection(ctx, id) }
func (s *Store) connection(ctx context.Context, id string) (Connection, error) {
	var c Connection
	var policy, settings []byte
	var sealed []byte
	var updated int64
	err := s.db.QueryRowContext(ctx, "SELECT c.id,c.name,c.enabled,c.policy,c.current_revision,r.digest,r.settings,c.key_ciphertext,c.key_hint,c.updated_at FROM connections c JOIN connection_revisions r ON r.id=c.id AND r.revision=c.current_revision WHERE c.id=?", id).Scan(&c.ID, &c.Name, &c.Enabled, &policy, &c.Current.Revision, &c.Current.Digest, &settings, &sealed, &c.KeyHint, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.Current.ID = c.ID
	c.KeySet = len(sealed) > 0
	c.UpdatedAt = time.UnixMilli(updated).UTC()
	if err = json.Unmarshal(policy, &c.Policy); err != nil {
		return c, err
	}
	return c, json.Unmarshal(settings, &c.Settings)
}

// Resolve returns the exact settings of one pinned revision with the current
// policy and key. Disabled connections still resolve so that retained calls can
// be reconciled; callers decide whether new work is allowed.
func (s *Store) Resolve(ctx context.Context, ref evolution.Revision) (Connection, Settings, string, error) {
	c, err := s.connection(ctx, ref.ID)
	if err != nil {
		return c, Settings{}, "", err
	}
	var raw []byte
	var digest string
	err = s.db.QueryRowContext(ctx, "SELECT digest,settings FROM connection_revisions WHERE id=? AND revision=?", ref.ID, ref.Revision).Scan(&digest, &raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && digest != ref.Digest {
		return c, Settings{}, "", ErrNotFound
	}
	if err != nil {
		return c, Settings{}, "", err
	}
	var settings Settings
	if err = json.Unmarshal(raw, &settings); err != nil {
		return c, settings, "", err
	}
	key, err := s.key(ctx, ref.ID)
	return c, settings, key, err
}
func (s *Store) key(ctx context.Context, id string) (string, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT key_ciphertext FROM connections WHERE id=?", id).Scan(&sealed); err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		return "", ErrNoKey
	}
	if s.aead == nil {
		return "", ErrNoSealKey
	}
	size := s.aead.NonceSize()
	if len(sealed) <= size {
		return "", ErrNoKey
	}
	plain, err := s.aead.Open(nil, sealed[:size], sealed[size:], []byte(id))
	if err != nil {
		return "", fmt.Errorf("model connection key cannot be decrypted with this host secret")
	}
	return string(plain), nil
}

// Revisions lists every pinned revision so retained workflows keep resolving.
func (s *Store) Revisions(ctx context.Context) ([]evolution.Revision, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,revision,digest FROM connection_revisions ORDER BY id,revision")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []evolution.Revision{}
	for rows.Next() {
		var ref evolution.Revision
		if err = rows.Scan(&ref.ID, &ref.Revision, &ref.Digest); err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

// RefString is the stable text form of a pinned model revision.
func RefString(ref evolution.Revision) string {
	return fmt.Sprintf("%s@%d@%s", ref.ID, ref.Revision, ref.Digest)
}

// Revision resolves a pinned revision without decrypting its key.
func (s *Store) Revision(ctx context.Context, ref evolution.Revision) (Connection, Settings, error) {
	c, err := s.connection(ctx, ref.ID)
	if err != nil {
		return c, Settings{}, err
	}
	var raw []byte
	var digest string
	if err = s.db.QueryRowContext(ctx, "SELECT digest,settings FROM connection_revisions WHERE id=? AND revision=?", ref.ID, ref.Revision).Scan(&digest, &raw); err != nil || digest != ref.Digest {
		return c, Settings{}, ErrNotFound
	}
	var settings Settings
	return c, settings, json.Unmarshal(raw, &settings)
}

// Choice is one model offered for new agent nodes.
type Choice struct {
	Ref   evolution.Revision `json:"ref"`
	Label string             `json:"label"`
	// CallTokens is reserved from the run budget for every call.
	CallTokens int64 `json:"call_tokens"`
}

// StoredKey decrypts a connection's key for an administrator's test request.
func (s *Store) StoredKey(ctx context.Context, id string) (string, error) { return s.key(ctx, id) }

// Instance identifies this process; a running call from another instance was
// interrupted by a restart.
func (s *Store) Instance() string { return s.instance }

// CallKey identifies one retained native model call.
type CallKey struct {
	RunID, CallID, IdempotencyKey, RequestDigest string
	Model                                        evolution.Revision
}
type CallRow struct {
	IdempotencyKey, RequestDigest, Model, State, Instance string
	CeilingTokens, CeilingCost                            int64
	Observation                                           []byte
}

var ErrCallNotFound = errors.New("model call not recorded")

func (s *Store) Call(ctx context.Context, key CallKey) (CallRow, error) {
	var row CallRow
	err := s.db.QueryRowContext(ctx, "SELECT idempotency_key,request_digest,model,state,instance,ceiling_tokens,ceiling_cost,coalesce(observation,x'') FROM calls WHERE run_id=? AND call_id=?", key.RunID, key.CallID).Scan(&row.IdempotencyKey, &row.RequestDigest, &row.Model, &row.State, &row.Instance, &row.CeilingTokens, &row.CeilingCost, &row.Observation)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrCallNotFound
	}
	return row, err
}

// InsertRunning records a call before dispatch; a concurrent insert conflicts.
func (s *Store) InsertRunning(ctx context.Context, key CallKey, ceilingTokens, ceilingCost int64) error {
	result, err := s.db.ExecContext(ctx, "INSERT INTO calls(run_id,call_id,idempotency_key,request_digest,model,state,instance,ceiling_tokens,ceiling_cost,created_at) VALUES(?,?,?,?,?,'running',?,?,?,?) ON CONFLICT(run_id,call_id) DO NOTHING", key.RunID, key.CallID, key.IdempotencyKey, key.RequestDigest, RefString(key.Model), s.instance, ceilingTokens, ceilingCost, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}

// InsertTerminal records a call that never reached the provider and returns
// whichever observation is retained for the call.
func (s *Store) InsertTerminal(ctx context.Context, key CallKey, state string, observation []byte) ([]byte, error) {
	if _, err := s.db.ExecContext(ctx, "INSERT INTO calls(run_id,call_id,idempotency_key,request_digest,model,state,instance,ceiling_tokens,ceiling_cost,observation,created_at) VALUES(?,?,?,?,?,?,?,0,0,?,?) ON CONFLICT(run_id,call_id) DO NOTHING", key.RunID, key.CallID, key.IdempotencyKey, key.RequestDigest, RefString(key.Model), state, s.instance, observation, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	row, err := s.Call(ctx, key)
	if err != nil || row.RequestDigest != key.RequestDigest || row.State == "running" {
		return nil, ErrConflict
	}
	return row.Observation, nil
}

// Finish settles a running call once; a later settle returns the first result.
func (s *Store) Finish(ctx context.Context, key CallKey, instance, state string, observation []byte) ([]byte, error) {
	if _, err := s.db.ExecContext(ctx, "UPDATE calls SET state=?,observation=? WHERE run_id=? AND call_id=? AND state='running' AND instance=?", state, observation, key.RunID, key.CallID, instance); err != nil {
		return nil, err
	}
	row, err := s.Call(ctx, key)
	if err != nil || row.State == "running" {
		return nil, ErrConflict
	}
	return row.Observation, nil
}

// Path is the database location, used to reopen the store as a new process.
func (s *Store) Path() string { return s.path }
