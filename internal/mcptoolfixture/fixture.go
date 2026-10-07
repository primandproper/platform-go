// Package mcptoolfixture holds the documented structs mcptool's suite
// extracts and reflects. Extract reads source by import path, which a _test.go
// file does not have.
package mcptoolfixture

import (
	"time"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Row is a documented struct for the extractor to read.
type Row struct {
	// CreatedAt is when the row was made.
	//
	// This second paragraph is reasoning, and is not extracted.
	CreatedAt time.Time `json:"createdAt"`
	// Note is optional prose.
	Note *string `json:"note,omitempty"`
	// Scope is whose row it is.
	Scope tenancy.Scope `json:"scope"`
	// Hidden is never marshaled.
	Hidden string `json:"-"`
	ID     string `json:"id"` // ID identifies the row.
	// Children are the row's children.
	Children []Child `json:"children"`
}

// Child is nested inside a Row.
type Child struct {
	// Name is the child's name.
	Name string `json:"name"`
}

// Page carries a filter, the way a list tool's input does.
type Page struct {
	// Filter is the page to read.
	Filter *filtering.QueryFilter `json:"filter,omitempty"`
}
