package version

import (
	"encoding/json"
	"io"
	"os"
)

const (
	unknownVersion = "unknown"
)

// Build-injected variables (set via -ldflags -X at build time).
var (
	Version    string // e.g. semver from release tag (v1.2.3)
	CommitHash string
	CommitTime string
	BuildTime  string
)

// Info holds VCS and build metadata for JSON output.
type Info struct {
	Version    string `json:"version"`
	CommitHash string `json:"commit_hash"`
	CommitTime string `json:"commit_time"`
	BuildTime  string `json:"build_time"`
}

// Get returns the current version info, using "unknown" for any unset field.
func Get() Info {
	version := Version
	if version == "" {
		version = unknownVersion
	}

	commitHash := CommitHash
	if commitHash == "" {
		commitHash = unknownVersion
	}

	commitTime := CommitTime
	if commitTime == "" {
		commitTime = unknownVersion
	}

	buildTime := BuildTime
	if buildTime == "" {
		buildTime = unknownVersion
	}

	return Info{
		Version:    version,
		CommitHash: commitHash,
		CommitTime: commitTime,
		BuildTime:  buildTime,
	}
}

// WriteJSON marshals the version info as indented JSON to w.
func WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Get())
}

// WriteJSONToStdout marshals the version info as indented JSON to os.Stdout, for CLI use.
func WriteJSONToStdout() error {
	return WriteJSON(os.Stdout)
}
