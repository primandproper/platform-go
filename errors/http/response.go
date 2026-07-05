package http

import (
	"fmt"

	"github.com/primandproper/platform-go/v3/filtering"
)

type (
	// ResponseDetails represents details about the response.
	ResponseDetails struct {
		_ struct{} `json:"-"`

		CurrentAccountID string `json:"currentAccountID"`
		TraceID          string `json:"traceID"`
	}

	// APIResponse represents a response we might send to the user.
	APIResponse[T any] struct {
		_ struct{} `json:"-"`

		Data       T                     `json:"data,omitempty"`
		Pagination *filtering.Pagination `json:"pagination,omitempty"`
		Error      *APIError             `json:"error,omitempty"`
		Details    ResponseDetails       `json:"details"`
	}

	// APIError represents a response we might send to the User in the event of an error.
	APIError struct {
		_ struct{} `json:"-"`

		Message string    `json:"message"`
		Code    ErrorCode `json:"code"`
	}
)

// Error returns the error message.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// AsError returns the error message.
func (e *APIError) AsError() error {
	if e == nil {
		return nil
	}
	return e
}

// NewAPIErrorResponse returns a new APIResponse with an error field.
func NewAPIErrorResponse(issue string, code ErrorCode, details ResponseDetails) *APIResponse[any] {
	return &APIResponse[any]{
		Error: &APIError{
			Message: issue,
			Code:    code,
		},
		Details: details,
	}
}
