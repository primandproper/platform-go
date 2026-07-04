package version

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestGet(T *testing.T) { //nolint:paralleltest // mutates package-level vars; subtests must run sequentially
	T.Run("returns unknown when vars are unset", func(t *testing.T) { //nolint:paralleltest // mutates package-level vars; subtests must run sequentially
		origVersion, origHash, origCTime, origBTime := Version, CommitHash, CommitTime, BuildTime
		Version, CommitHash, CommitTime, BuildTime = "", "", "", ""
		t.Cleanup(func() {
			Version, CommitHash, CommitTime, BuildTime = origVersion, origHash, origCTime, origBTime
		})

		info := Get()
		test.EqOp(t, "unknown", info.Version)
		test.EqOp(t, "unknown", info.CommitHash)
		test.EqOp(t, "unknown", info.CommitTime)
		test.EqOp(t, "unknown", info.BuildTime)
	})

	T.Run("returns set values when vars are populated", func(t *testing.T) { //nolint:paralleltest // mutates package-level vars; subtests must run sequentially
		origVersion, origHash, origCTime, origBTime := Version, CommitHash, CommitTime, BuildTime
		Version = "v1.2.3"
		CommitHash = "abc123"
		CommitTime = "2026-01-01T00:00:00Z"
		BuildTime = "2026-01-02T00:00:00Z"
		t.Cleanup(func() {
			Version, CommitHash, CommitTime, BuildTime = origVersion, origHash, origCTime, origBTime
		})

		info := Get()
		test.EqOp(t, "v1.2.3", info.Version)
		test.EqOp(t, "abc123", info.CommitHash)
		test.EqOp(t, "2026-01-01T00:00:00Z", info.CommitTime)
		test.EqOp(t, "2026-01-02T00:00:00Z", info.BuildTime)
	})
}

func TestWriteJSON(T *testing.T) { //nolint:paralleltest // mutates package-level vars; subtests must run sequentially
	T.Run("writes the current version info as JSON to the writer", func(t *testing.T) {
		origVersion, origHash, origCTime, origBTime := Version, CommitHash, CommitTime, BuildTime
		Version = "v9.9.9"
		CommitHash = "deadbeef"
		CommitTime = "2026-03-03T00:00:00Z"
		BuildTime = "2026-03-04T00:00:00Z"
		t.Cleanup(func() {
			Version, CommitHash, CommitTime, BuildTime = origVersion, origHash, origCTime, origBTime
		})

		var buf bytes.Buffer
		must.NoError(t, WriteJSON(&buf))

		var got Info
		must.NoError(t, json.Unmarshal(buf.Bytes(), &got))

		test.Eq(t, Get(), got)
		test.EqOp(t, "v9.9.9", got.Version)
		test.EqOp(t, "deadbeef", got.CommitHash)
	})
}
