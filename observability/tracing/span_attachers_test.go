package tracing

import (
	"errors"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/database/filtering"

	"github.com/shoenig/test/must"
)

// mockServicePermissionChecker implements ServicePermissionChecker for tests.
type mockServicePermissionChecker struct {
	isAdmin bool
}

func (m mockServicePermissionChecker) IsServiceAdmin() bool {
	return m.isAdmin
}

// mockSessionContextData implements SessionContextDataForTracing for tests.
type mockSessionContextData struct {
	checker   ServicePermissionChecker
	userID    string
	accountID string
}

func (m *mockSessionContextData) GetUserID() string {
	return m.userID
}

func (m *mockSessionContextData) GetServicePermissions() ServicePermissionChecker {
	return m.checker
}

func (m *mockSessionContextData) GetActiveAccountID() string {
	return m.accountID
}

func TestAttachSessionContextDataToSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		_, span := StartSpan(t.Context())

		AttachSessionContextDataToSpan(span, &mockSessionContextData{
			userID:    "user-1",
			accountID: "account-1",
			checker:   mockServicePermissionChecker{isAdmin: false},
		})
	})
}

func TestAttachRequestToSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx, span := StartSpan(t.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)
		req.Header.Set(t.Name(), "blah")
		must.NoError(t, err)

		AttachRequestToSpan(span, req)
	})
}

func TestAttachResponseToSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		_, span := StartSpan(t.Context())
		res := &http.Response{
			Header: map[string][]string{},
		}
		res.Header.Set(t.Name(), "blah")

		AttachResponseToSpan(span, res)
	})
}

func TestAttachErrorToSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		_, span := StartSpan(t.Context())

		AttachErrorToSpan(span, t.Name(), errors.New("blah"))
	})
}

func TestAttachQueryFilterToSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		_, span := StartSpan(t.Context())

		AttachQueryFilterToSpan(span, filtering.DefaultQueryFilter())
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		_, span := StartSpan(t.Context())

		AttachQueryFilterToSpan(span, nil)
	})
}

func TestAttachToSpan(T *testing.T) {
	T.Parallel()

	type testCase struct {
		x             any
		name          string
		attachmentKey string
	}
	tests := []testCase{

		{
			name:          "int",
			attachmentKey: T.Name(),
			x:             1,
		},
		{
			name:          "int slice",
			attachmentKey: T.Name(),
			x:             []int{1},
		},
		{
			name:          "int8",
			attachmentKey: T.Name(),
			x:             int8(1),
		},
		{
			name:          "int8 slice",
			attachmentKey: T.Name(),
			x:             []int8{int8(1)},
		},
		{
			name:          "int16",
			attachmentKey: T.Name(),
			x:             int16(1),
		},
		{
			name:          "int16 slice",
			attachmentKey: T.Name(),
			x:             []int16{int16(1)},
		},
		{
			name:          "int32",
			attachmentKey: T.Name(),
			x:             int32(1),
		},
		{
			name:          "int32 slice",
			attachmentKey: T.Name(),
			x:             []int32{int32(1)},
		},
		{
			name:          "int64",
			attachmentKey: T.Name(),
			x:             int64(1),
		},
		{
			name:          "int64 slice",
			attachmentKey: T.Name(),
			x:             []int64{int64(1)},
		},
		{
			name:          "uint",
			attachmentKey: T.Name(),
			x:             uint(1),
		},
		{
			name:          "uint slice",
			attachmentKey: T.Name(),
			x:             []uint{uint(1)},
		},
		{
			name:          "uint8",
			attachmentKey: T.Name(),
			x:             uint8(1),
		},
		{
			name:          "uint8 slice",
			attachmentKey: T.Name(),
			x:             []uint8{uint8(1)},
		},
		{
			name:          "uint16",
			attachmentKey: T.Name(),
			x:             uint16(1),
		},
		{
			name:          "uint16 slice",
			attachmentKey: T.Name(),
			x:             []uint16{uint16(1)},
		},
		{
			name:          "uint32",
			attachmentKey: T.Name(),
			x:             uint32(1),
		},
		{
			name:          "uint32 slice",
			attachmentKey: T.Name(),
			x:             []uint32{uint32(1)},
		},
		{
			name:          "uint64",
			attachmentKey: T.Name(),
			x:             uint64(1),
		},
		{
			name:          "uint64 slice",
			attachmentKey: T.Name(),
			x:             []uint64{uint64(1)},
		},
		{
			name:          "float32",
			attachmentKey: T.Name(),
			x:             float32(1),
		},
		{
			name:          "float32 slice",
			attachmentKey: T.Name(),
			x:             []float32{float32(1)},
		},
		{
			name:          "float64",
			attachmentKey: T.Name(),
			x:             float64(1),
		},
		{
			name:          "float64 slice",
			attachmentKey: T.Name(),
			x:             []float64{float64(1)},
		},
		{
			name:          "string",
			attachmentKey: T.Name(),
			x:             "test",
		},
		{
			name:          "string slice",
			attachmentKey: T.Name(),
			x:             []string{"test"},
		},
		{
			name:          "bool",
			attachmentKey: T.Name(),
			x:             true,
		},
		{
			name:          "bool slice",
			attachmentKey: T.Name(),
			x:             []bool{true},
		},
	}
	for _, tt := range tests {
		T.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, span := StartSpan(t.Context())
			AttachToSpan(span, tt.attachmentKey, tt.x)
		})
	}
}
