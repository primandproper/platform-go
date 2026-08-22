package dataprivacy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/dataprivacy/migrations"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/operations"
	operationsmock "github.com/primandproper/platform-go/v13/operations/mock"
	"github.com/primandproper/platform-go/v13/uploads"

	"github.com/shoenig/test/must"
)

// baseTime is the instant this suite works relative to.
var baseTime = time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// stubClock is a manually advanced clock. Expiry, deadlines, and confirmation
// windows are all functions of elapsed time and these tests need days of it, so
// they control the clock rather than race the wall.
//
// A synctest bubble would normally spare us a double, but it advances fake time
// only once every goroutine in the bubble is durably blocked, and these tests
// drive a real SQLite file. Built on the generated mock so the methods nothing
// calls fail loudly instead of lying.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: baseTime}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:       c.read,
		SinceFunc:     func(t time.Time) time.Duration { return c.read().Sub(t) },
		NewTickerFunc: clock.NewClock().NewTicker,
	}

	return c
}

func (c *stubClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *stubClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// prefixCounter names a fresh table per subtest. Subtests share one database
// and must not share tables — the claim predicate is global to the requests
// table, so one test's backlog would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real
// SQL — placeholder rendering, the claim predicate, the guarded transitions,
// the partial indexes — without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "dataprivacy.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed request table and returns a Store over
// it.
func (e *storeEnv) newStore(t *testing.T) Store {
	t.Helper()

	prefix := fmt.Sprintf("dp_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := NewSQLStore(e.client, WithTablePrefix(prefix))
	must.NoError(t, err)

	return store
}

// saveRequest inserts a request through a transaction, as the Service does.
func saveRequest(t *testing.T, store Store, req *Request) *Request {
	t.Helper()

	must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		return store.Save(t.Context(), q, req)
	}))

	return req
}

// newRequest builds an in-progress request of the given type, as Submit leaves
// one: an operation exists and is fulfilling it.
func newRequest(id string, t RequestType, subject Subject, at time.Time) *Request {
	return &Request{
		ID:          id,
		Type:        t,
		Subject:     subject,
		Status:      StatusInProgress,
		OperationID: "op-" + id,
		RequestedAt: at,
		DueAt:       at.Add(DefaultResponseWindow),
	}
}

// recordingReporter is a Reporter that records what a Runner said.
//
// It is hand-written rather than generated for the reason operations/mock gives:
// a test of a Runner wants to observe what the Runner reported, and the honest
// way to do that is a small recording implementation of methods that only
// append.
type recordingReporter struct {
	cancelled chan struct{}

	units    []string
	messages []string

	attempt operations.Attempt

	mu sync.Mutex

	count     int64
	total     int
	unitsDone int
	totalSet  bool
}

var _ operations.Reporter = (*recordingReporter)(nil)

func newRecordingReporter(attempt operations.Attempt) *recordingReporter {
	return &recordingReporter{cancelled: make(chan struct{}), attempt: attempt}
}

// newFinalReporter is the reporter for a test that wants a failure recorded
// rather than retried, which is most of them: the retry path writes nothing to
// the request row, so a non-final attempt leaves nothing to assert on.
func newFinalReporter() *recordingReporter {
	return newRecordingReporter(operations.Attempt{ID: "op-1", Number: DefaultMaxAttempts, Final: true})
}

func (r *recordingReporter) SetUnits(total int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.total, r.totalSet = total, true
}

func (r *recordingReporter) StartUnit(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.units = append(r.units, name)
}

func (r *recordingReporter) FinishUnit() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.unitsDone++
}

func (r *recordingReporter) Advance(n int64) {
	if n <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.count += n
}

func (r *recordingReporter) Sayf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

func (r *recordingReporter) Cancelled() <-chan struct{} { return r.cancelled }

func (r *recordingReporter) Attempt() operations.Attempt { return r.attempt }

// cancel asks the runner to stop, as a flush that observed the row's
// cancellation flag would.
func (r *recordingReporter) cancel() { close(r.cancelled) }

// progress reads the tiers back under the lock.
func (r *recordingReporter) progress() (total int, totalSet bool, unitsDone int, count int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.total, r.totalSet, r.unitsDone, r.count
}

// startedUnits reports the units a runner opened, sorted, since a concurrent
// fan-out opens them in whatever order the pool hands out slots.
func (r *recordingReporter) startedUnits() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Sorted(slices.Values(r.units))
}

// stubOperations is an operations.Service that records what was started and
// hands back plausible operations.
//
// The dataprivacy Service only ever asks it to start, enqueue, and cancel, so
// the rest of the interface is left to the generated mock's nil funcs — which
// panic if anything reaches them, which is the point.
type stubOperations struct {
	*operationsmock.ServiceMock

	startErr    error
	enqueueErr  error
	cancelErr   error
	started     []startedOperation
	enqueued    []string
	cancelled   []string
	nextIDIndex atomic.Int64

	mu sync.Mutex
}

// startedOperation is one call to StartInTransaction.
//
// It records the kind and the request and not the options, because a
// StartOption is an opaque closure over an unexported struct and there is no
// honest way to read one back. That the operation's owner is the subject is
// asserted where a real operations Service is wired, in the container tests.
type startedOperation struct {
	request any
	kind    string
}

func newStubOperations() *stubOperations {
	s := &stubOperations{ServiceMock: &operationsmock.ServiceMock{}}

	s.StartInTransactionFunc = func(
		_ context.Context,
		_ database.SQLQueryExecutor,
		kind string,
		request any,
		_ ...operations.StartOption,
	) (*operations.Operation, error) {
		if s.startErr != nil {
			return nil, s.startErr
		}

		op := &operations.Operation{
			ID:    fmt.Sprintf("op-%d", s.nextIDIndex.Add(1)),
			Kind:  kind,
			State: operations.StatePending,
		}

		s.mu.Lock()
		defer s.mu.Unlock()

		s.started = append(s.started, startedOperation{kind: kind, request: request})

		return op, nil
	}

	s.EnqueueFunc = func(_ context.Context, id string, _ ...operations.StartOption) error {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.enqueued = append(s.enqueued, id)

		return s.enqueueErr
	}

	s.CancelFunc = func(_ context.Context, id string) (*operations.Operation, error) {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.cancelled = append(s.cancelled, id)

		if s.cancelErr != nil {
			return nil, s.cancelErr
		}

		return &operations.Operation{ID: id, State: operations.StateCancelled}, nil
	}

	return s
}

func (s *stubOperations) startedOperations() []startedOperation {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.started)
}

func (s *stubOperations) enqueuedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.enqueued)
}

func (s *stubOperations) cancelledIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.cancelled)
}

// testSubject is the subject most of this suite is about.
var testSubject = Subject{ID: "user-1", Type: SubjectUser, Scope: "account-1"}

// memoryUploader is an in-process UploadManager. It records what was written so
// a test can assert the artifact's bytes, and implements Delete/Exists so the
// sweeper's already-gone path is reachable.
type memoryUploader struct {
	objects map[string][]byte
	types   map[string]string

	deleteErr error

	mu sync.Mutex
}

var _ uploads.UploadManager = (*memoryUploader)(nil)

func newMemoryUploader() *memoryUploader {
	return &memoryUploader{
		objects: map[string][]byte{},
		types:   map[string]string{},
	}
}

func (m *memoryUploader) Save(_ context.Context, path string, r io.Reader, opts ...uploads.SaveOption) error {
	content, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.objects[path] = content
	m.types[path] = uploads.BuildSaveOptions(opts...).ContentType

	return nil
}

func (m *memoryUploader) Open(_ context.Context, path string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	content, ok := m.objects[path]
	if !ok {
		return nil, platformerrors.Newf("no such object %q", path)
	}

	return io.NopCloser(bytes.NewReader(content)), nil
}

func (m *memoryUploader) Delete(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deleteErr != nil {
		return m.deleteErr
	}

	if _, ok := m.objects[path]; !ok {
		return platformerrors.Newf("no such object %q", path)
	}

	delete(m.objects, path)

	return nil
}

func (m *memoryUploader) Exists(_ context.Context, path string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.objects[path]

	return ok, nil
}

func (m *memoryUploader) get(path string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	content, ok := m.objects[path]

	return content, ok
}

func (m *memoryUploader) paths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Sorted(maps.Keys(m.objects))
}

// signingUploader is a memoryUploader that can also sign, for the Download
// path. Kept separate so a test can exercise the "provider cannot sign" branch
// by using the plain one.
type signingUploader struct {
	*memoryUploader
}

var (
	_ uploads.UploadManager = (*signingUploader)(nil)
	_ uploads.URLSigner     = (*signingUploader)(nil)
)

func (s *signingUploader) SignedURL(_ context.Context, path string, opts *uploads.SignedURLOptions) (string, error) {
	expiry := time.Duration(0)
	if opts != nil {
		expiry = opts.Expiry
	}

	return fmt.Sprintf("https://storage.example/%s?expires_in=%s", path, expiry), nil
}

// staticCollector returns fixed bytes.
func staticCollector(fragment string) Collector {
	return CollectorFunc(func(context.Context, Subject) (json.RawMessage, error) {
		return json.RawMessage(fragment), nil
	})
}

// failingCollector always errors.
func failingCollector(err error) Collector {
	return CollectorFunc(func(context.Context, Subject) (json.RawMessage, error) {
		return nil, err
	})
}

// countingEraser reports a fixed outcome and records that it ran.
func countingEraser(deleted, anonymized int64, retained map[string]string, ran *atomic.Int64) Eraser {
	return EraserFunc(func(context.Context, database.SQLQueryExecutor, Subject) (ErasureOutcome, error) {
		if ran != nil {
			ran.Add(1)
		}

		return ErasureOutcome{Deleted: deleted, Anonymized: anonymized, Retained: retained}, nil
	})
}

// failingOverdueStore fails only the overdue count, so a sweep's other chores
// still run and the partial result is observable.
type failingOverdueStore struct {
	Store
}

func (s *failingOverdueStore) CountOverdue(context.Context, time.Time) (map[RequestType]int64, error) {
	return nil, platformerrors.New("the read replica is unreachable")
}

// stringReader is a small convenience for writing fixture objects.
func stringReader(content string) io.Reader {
	return bytes.NewReader([]byte(content))
}

// decodeArtifact reads a stored artifact back into a Document.
func decodeArtifact(t *testing.T, p *packager, stored []byte) *Document {
	t.Helper()

	decoded, err := p.decode(t.Context(), stored, testRequestID)
	must.NoError(t, err)

	var doc Document
	must.NoError(t, json.Unmarshal(decoded, &doc))

	return &doc
}

// Close satisfies uploads.UploadManager.
func (u *memoryUploader) Close() error { return nil }

// Close satisfies uploads.UploadManager.
func (u *signingUploader) Close() error { return nil }
