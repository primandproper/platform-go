package operations

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/conformance/internal/httpcall"
	operationshttp "github.com/primandproper/platform-go/v15/operations/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "operations"

const (
	// pageSize is how many operations a listing asks for at a time.
	pageSize = 50

	// pageBudget is how many pages a listing walks looking for one operation
	// before it says the operation is not there. The walk is filtered by kind,
	// so in any deployment a suite can be run against this is many times what
	// one caller's operations of one kind fill.
	pageBudget = 20

	// streamBudget is how long a subscription may take to deliver its first
	// snapshot, which the Watcher sends at subscribe time.
	streamBudget = 10 * time.Second

	// stateCancelled is a cancelled operation's state, as the wire spells it.
	stateCancelled = "cancelled"
)

// Suite is the operations surface's behavioral assertions: that the read, the
// listing, the cancellation and the event stream each answer an operation's
// owners and nobody else.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name: surface,

		// The HTTP surfaces are not in Surfaces; whether this one is served is
		// read off the probe caller's HTTP inside, and skipped with the reason.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     run,
	}
}

// operation is an operation as a caller is shown it, reduced to what the
// assertions read.
type operation struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`

	// CancelRequested is what a cancellation writes on an operation that has
	// started, which it does not stop on the spot: the state stays running
	// until the work notices.
	CancelRequested bool `json:"cancelRequested"`
}

// page is one page of a listing.
type page struct {
	Cursor string      `json:"cursor"`
	Data   []operation `json:"data"`
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)
	if probe.HTTP == nil || !probe.HTTP.Operations {
		conformance.Skip(t, "conformance: this subject does not serve the operations surface")
	}

	t.Run("person-owned", func(t *testing.T) {
		t.Parallel()

		personOwned(t, s, probe)
	})

	t.Run("tenant-owned", func(t *testing.T) {
		t.Parallel()

		tenantOwned(t, s)
	})
}

// path is the operation's own route, with suffix after it.
func path(id, suffix string) string {
	return operationshttp.BasePath + "/" + url.PathEscape(id) + suffix
}

// read reads one operation as caller, and returns what it answered.
func read(t *testing.T, caller *conformance.Subject, id string) (status int, op *operation) {
	t.Helper()

	status, body := httpcall.Call(t, caller, http.MethodGet, path(id, ""), nil)
	if status != http.StatusOK {
		return status, nil
	}

	out := &httpcall.Envelope[operation]{}
	must.NoError(t, json.Unmarshal(body, out), must.Sprintf("reading operation %s: %s", id, body))

	return status, &out.Data
}

// cancel asks for an operation's cancellation as caller, and returns what it
// answered.
func cancel(t *testing.T, caller *conformance.Subject, id string) (status int, op *operation) {
	t.Helper()

	status, body := httpcall.Call(t, caller, http.MethodPost, path(id, operationshttp.CancelSuffix), []byte("{}"))
	if status != http.StatusOK {
		return status, nil
	}

	out := &httpcall.Envelope[operation]{}
	must.NoError(t, json.Unmarshal(body, out), must.Sprintf("cancelling operation %s: %s", id, body))

	return status, &out.Data
}

// listed reports whether caller's listing of kind holds the operation id,
// walking it a page at a time.
//
// Paged rather than read off the first page, because a caller's listing is the
// union of every owner they may read, merged in identifier order and cut at the
// page size: in a deployment whose tenant has run plenty, the operation the
// test just started may sit several pages in. Filtered by kind for the same
// reason.
func listed(t *testing.T, caller *conformance.Subject, kind, id string) bool {
	t.Helper()

	query := url.Values{"kind": {kind}, "limit": {strconv.Itoa(pageSize)}}

	for range pageBudget {
		status, body := httpcall.Call(t, caller, http.MethodGet, operationshttp.BasePath+"?"+query.Encode(), nil)
		must.EqOp(t, http.StatusOK, status, must.Sprintf("listing operations answered %d: %s", status, body))

		out := &httpcall.Envelope[page]{}
		must.NoError(t, json.Unmarshal(body, out), must.Sprintf("listing operations: %s", body))

		for i := range out.Data.Data {
			if out.Data.Data[i].ID == id {
				return true
			}
		}

		// A short page is the last one, and a cursor that did not move would
		// read the same page forever.
		if len(out.Data.Data) < pageSize || out.Data.Cursor == "" || out.Data.Cursor == query.Get("cursor") {
			return false
		}

		query.Set("cursor", out.Data.Cursor)
	}

	return false
}

// subscribe opens an operation's event stream as caller, and returns the status
// it answered and, where it opened, the payload of its first operation frame.
//
// A refusal comes before any stream: the surface subscribes before it upgrades,
// so somebody else's operation is an ordinary 404. Heartbeats are skipped
// rather than refused, because one can precede the first snapshot when the read
// behind it is slow.
func subscribe(t *testing.T, caller *conformance.Subject, id string) (status int, snapshot string) {
	t.Helper()

	ctx, stop := context.WithTimeout(t.Context(), streamBudget)
	defer stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpcall.URL(caller, path(id, operationshttp.EventsSuffix)), http.NoBody)
	must.NoError(t, err)

	req.Header.Set("Accept", "text/event-stream")

	res, err := caller.HTTP.Client.Do(req)
	must.NoError(t, err, must.Sprintf("subscribing to operation %s", id))

	defer func() {
		// The stream runs until the operation finishes, so it is ended here
		// rather than read to its end.
		stop()
		test.NoError(t, res.Body.Close())
	}()

	if res.StatusCode != http.StatusOK {
		_, err = io.Copy(io.Discard, res.Body)
		must.NoError(t, err)

		return res.StatusCode, ""
	}

	must.StrHasPrefix(t, "text/event-stream", res.Header.Get("Content-Type"),
		must.Sprint("the event stream opened as something other than an event stream"))

	lines := bufio.NewScanner(res.Body)
	lines.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var (
		event string
		data  []string
	)

	for lines.Scan() {
		line := lines.Text()

		switch {
		case line == "":
			if event == operationshttp.EventOperation && len(data) > 0 {
				return res.StatusCode, strings.Join(data, "\n")
			}

			event, data = "", nil
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}

	t.Fatalf("the event stream for operation %s ended before it sent a snapshot: %v", id, lines.Err())

	return res.StatusCode, ""
}
