package noop

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v6/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewLogger(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		must.NotNil(t, l)
	})

	T.Run("returns same singleton", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, NewLogger(), NewLogger())
	})
}

func TestLogger_Info(T *testing.T) {
	T.Parallel()

	T.Run("does not panic", func(t *testing.T) {
		t.Parallel()

		test.NotPanic(t, func() { NewLogger().Info("message") })
	})
}

func TestLogger_Debug(T *testing.T) {
	T.Parallel()

	T.Run("does not panic", func(t *testing.T) {
		t.Parallel()

		test.NotPanic(t, func() { NewLogger().Debug("message") })
	})
}

func TestLogger_Error(T *testing.T) {
	T.Parallel()

	T.Run("does not panic", func(t *testing.T) {
		t.Parallel()

		test.NotPanic(t, func() { NewLogger().Error("something failed", errors.New("err")) })
	})
}

func TestLogger_SetRequestIDFunc(T *testing.T) {
	T.Parallel()

	T.Run("does not panic", func(t *testing.T) {
		t.Parallel()

		test.NotPanic(t, func() { NewLogger().SetRequestIDFunc(func(*http.Request) string { return "" }) })
	})
}

func TestLogger_Clone(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.Clone())
	})
}

func TestLogger_WithName(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.WithName("name"))
	})
}

func TestLogger_WithValues(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.WithValues(map[string]any{"key": "value"}))
	})
}

func TestLogger_WithValue(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.WithValue("key", "value"))
	})
}

func TestLogger_WithRequest(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		test.Eq(t, l, l.WithRequest(r))
	})
}

func TestLogger_WithResponse(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.WithResponse(&http.Response{}))
	})
}

func TestLogger_WithError(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.WithError(errors.New("err")))
	})
}

func TestLogger_WithSpan(T *testing.T) {
	T.Parallel()

	T.Run("returns same logger", func(t *testing.T) {
		t.Parallel()

		l := NewLogger()
		test.Eq(t, l, l.WithSpan(nil))
	})
}
