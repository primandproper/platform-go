package routing_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	httpx "github.com/primandproper/platform-go/v6/errors/http"
	"github.com/primandproper/platform-go/v6/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file pins down the shape of the OpenAPI document *as a whole* for a
// small, representative set of routes — as opposed to schema_test.go, which
// checks individual field schemas in isolation. Two complementary checks:
//
//   - TestSchema_DocumentStructure asserts the document's structure (paths,
//     operations, statuses, and the exact set of component schemas). It is
//     resilient to swaggest formatting changes.
//   - TestSchema_GoldenSpec compares the entire marshaled spec byte-for-byte
//     against a committed golden file, doubling as human-readable documentation
//     of the generated output. Regenerate it with:
//
//     UPDATE_GOLDEN=1 go test ./routing/ -run TestSchema_GoldenSpec

// referenceUser is the output type for the reference routes.
type referenceUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	ID    uint64 `json:"id"`
}

// createReferenceUserInput mixes a path param with body fields.
type createReferenceUserInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	OrgID uint64 `path:"orgID"`
}

// referenceStatus is a raw (non-enveloped) output body.
type referenceStatus struct {
	Version string `json:"version"`
	OK      bool   `json:"ok"`
}

// buildReferenceRouter wires a compact, representative API: an enveloped create
// with an extra error response, an enveloped fetch, a raw (non-enveloped) health
// check, and a bodyless delete. Both document-level tests build from this so
// they describe the same API.
func buildReferenceRouter(t *testing.T) *routing.Router {
	t.Helper()

	r := buildTestRouter(t, routing.WithTitle("Reference API"), routing.WithVersion("1.2.3"))

	routing.Post(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, _ createReferenceUserInput) (referenceUser, error) {
		return referenceUser{}, nil
	},
		routing.WithSummary("Create a user"),
		routing.WithTags("users"),
		routing.WithAdditionalResponse(http.StatusNotFound, httpx.APIError{}, "not found"),
	)

	routing.Get(r, "/orgs/{orgID:uint64}/users/{userID:uint64}", func(_ context.Context, _ struct {
		OrgID  uint64 `path:"orgID"`
		UserID uint64 `path:"userID"`
	}) (referenceUser, error) {
		return referenceUser{}, nil
	}, routing.WithSummary("Fetch a user"), routing.WithTags("users"))

	routing.Get(r, "/health", func(_ context.Context, _ routing.Empty) (referenceStatus, error) {
		return referenceStatus{}, nil
	}, routing.WithEnvelope(false), routing.WithTags("system"))

	// A route over the full-spectrum type from schema_test.go, so the document
	// structure and golden snapshot exercise the whole reflected schema surface
	// (every scalar, pointers, slices, arrays, maps, nested/embedded structs, ...).
	routing.Post(r, "/stress", func(_ context.Context, in stressAllTypes) (stressAllTypes, error) {
		return in, nil
	}, routing.WithSummary("Full-spectrum type"), routing.WithTags("stress"))

	routing.Delete(r, "/orgs/{orgID:uint64}/users/{userID:uint64}", func(_ context.Context, _ struct {
		OrgID  uint64 `path:"orgID"`
		UserID uint64 `path:"userID"`
	}) (routing.Empty, error) {
		return routing.Empty{}, nil
	}, routing.WithResponseStatus(http.StatusNoContent), routing.WithTags("users"))

	return r
}

// sortedKeys returns the keys of a JSON object, sorted, for order-independent
// set comparison.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}

// toStrings converts a JSON array of strings to a []string.
func toStrings(t *testing.T, v any) []string {
	t.Helper()

	raw, ok := v.([]any)
	must.True(t, ok, must.Sprintf("value is not an array: %v", v))

	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, isStr := e.(string)
		must.True(t, isStr, must.Sprintf("array element is not a string: %v", e))
		out = append(out, s)
	}

	return out
}

func TestSchema_DocumentStructure(T *testing.T) {
	T.Parallel()

	doc := specDoc(T, buildReferenceRouter(T))

	T.Run("top-level document", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "3.0.3", str(t, doc, "openapi"))

		info := dig(t, doc, "info")
		test.EqOp(t, "Reference API", str(t, info, "title"))
		test.EqOp(t, "1.2.3", str(t, info, "version"))
	})

	T.Run("exact set of paths", func(t *testing.T) {
		t.Parallel()

		want := []string{
			"/health",
			"/orgs/{orgID}/users",
			"/orgs/{orgID}/users/{userID}",
			"/stress",
		}
		test.Eq(t, want, sortedKeys(dig(t, doc, "paths")))
	})

	T.Run("operations", func(t *testing.T) {
		t.Parallel()

		type opWant struct {
			method      string
			operationID string
			tags        []string
			statuses    []string
			hasBody     bool
		}
		cases := map[string][]opWant{
			"/health": {
				{method: "get", operationID: "get_health", tags: []string{"system"}, statuses: []string{"200"}, hasBody: false},
			},
			"/orgs/{orgID}/users": {
				{method: "post", operationID: "post_orgs_orgID_users", tags: []string{"users"}, statuses: []string{"201", "404"}, hasBody: true},
			},
			"/orgs/{orgID}/users/{userID}": {
				{method: "get", operationID: "get_orgs_orgID_users_userID", tags: []string{"users"}, statuses: []string{"200"}, hasBody: false},
				{method: "delete", operationID: "delete_orgs_orgID_users_userID", tags: []string{"users"}, statuses: []string{"204"}, hasBody: false},
			},
			"/stress": {
				{method: "post", operationID: "post_stress", tags: []string{"stress"}, statuses: []string{"201"}, hasBody: true},
			},
		}

		for path, ops := range cases {
			item := dig(t, doc, "paths", path)
			for _, w := range ops {
				op := dig(t, item, w.method)

				test.EqOp(t, w.operationID, str(t, op, "operationId"), test.Sprintf("%s %s", w.method, path))
				test.Eq(t, w.tags, toStrings(t, op["tags"]), test.Sprintf("%s %s tags", w.method, path))
				test.Eq(t, w.statuses, sortedKeys(dig(t, op, "responses")), test.Sprintf("%s %s statuses", w.method, path))

				_, hasBody := op["requestBody"]
				test.EqOp(t, w.hasBody, hasBody, test.Sprintf("%s %s requestBody presence", w.method, path))
			}
		}
	})

	T.Run("exact set of component schemas", func(t *testing.T) {
		t.Parallel()

		want := []string{
			"FilteringPagination",
			"FilteringQueryFilter",
			"HttpAPIError",
			"HttpAPIResponseGithubComPrimandproperPlatformGoV6RoutingTestReferenceUser",
			"HttpAPIResponseGithubComPrimandproperPlatformGoV6RoutingTestStressAllTypes",
			"HttpResponseDetails",
			"RoutingTestCreateReferenceUserInput",
			"RoutingTestReferenceStatus",
			"RoutingTestReferenceUser",
			"RoutingTestStressAllTypes",
			"RoutingTestStressInner",
			"RoutingTestStressMiddle",
		}
		test.Eq(t, want, sortedKeys(schemas(t, doc)))
	})

	T.Run("bodyless response documents no content", func(t *testing.T) {
		t.Parallel()

		del := dig(t, doc, "paths", "/orgs/{orgID}/users/{userID}", "delete", "responses", "204")
		test.EqOp(t, "No Content", str(t, del, "description"))
		test.MapNotContainsKey(t, del, "content")
	})
}

func TestSchema_GoldenSpec(T *testing.T) {
	T.Parallel()

	const goldenPath = "testdata/reference_spec.golden.json"

	got, err := buildReferenceRouter(T).MarshalSpec()
	must.NoError(T, err)
	got = append(got, '\n') // keep the golden file newline-terminated

	if os.Getenv("UPDATE_GOLDEN") != "" {
		must.NoError(T, os.MkdirAll("testdata", 0o750))
		must.NoError(T, os.WriteFile(goldenPath, got, 0o600))
		T.Logf("wrote %s (%d bytes)", goldenPath, len(got))

		return
	}

	want, err := os.ReadFile(goldenPath)
	must.NoError(T, err, must.Sprint("missing golden file; regenerate with: UPDATE_GOLDEN=1 go test ./routing/ -run TestSchema_GoldenSpec"))

	if !bytes.Equal(got, want) {
		// Line-split diff renders far more readably than one giant string.
		test.Eq(T, strings.Split(string(want), "\n"), strings.Split(string(got), "\n"))
		T.Log("golden spec mismatch; if this change is intended, regenerate with: UPDATE_GOLDEN=1 go test ./routing/ -run TestSchema_GoldenSpec")
	}
}
