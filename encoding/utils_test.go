package encoding

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestDecode(T *testing.T) {
	T.Parallel()

	T.Run("with nil content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.NoError(t, Decode([]byte(`{"name":"test"}`), nil, &dest))
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with explicit content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.NoError(t, Decode([]byte(`<example><name>test</name></example>`), ContentTypeXML, &dest))
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with TOML content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.NoError(t, Decode([]byte(`name = "test"`+"\n"), ContentTypeTOML, &dest))
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with YAML content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.NoError(t, Decode([]byte("name: test\n"), ContentTypeYAML, &dest))
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with invalid data", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.Error(t, Decode([]byte(`{invalid`), nil, &dest))
	})
}

func TestMustEncode(T *testing.T) {
	T.Parallel()

	T.Run("with nil content type", func(t *testing.T) {
		t.Parallel()

		result := MustEncode(&example{Name: t.Name()}, nil)
		test.SliceNotEmpty(t, result)
	})

	T.Run("with explicit content type", func(t *testing.T) {
		t.Parallel()

		result := MustEncode(&example{Name: t.Name()}, ContentTypeXML)
		test.SliceNotEmpty(t, result)
	})

	T.Run("panics with un-encodable data", func(t *testing.T) {
		t.Parallel()

		defer func() {
			test.NotNil(t, recover())
		}()

		MustEncode(&broken{Name: json.Number(t.Name())}, nil)
	})
}

func TestMustDecode(T *testing.T) {
	T.Parallel()

	T.Run("with nil content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		MustDecode([]byte(`{"name":"test"}`), nil, &dest)
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with explicit content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		MustDecode([]byte(`<example><name>test</name></example>`), ContentTypeXML, &dest)
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with YAML content type", func(t *testing.T) {
		t.Parallel()

		var dest example
		MustDecode([]byte("name: test\n"), ContentTypeYAML, &dest)
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("panics with invalid data", func(t *testing.T) {
		t.Parallel()

		defer func() {
			test.NotNil(t, recover())
		}()

		var dest example
		MustDecode([]byte(`{invalid`), nil, &dest)
	})
}

func TestMustEncodeJSON(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		result := MustEncodeJSON(&example{Name: t.Name()})
		test.SliceNotEmpty(t, result)
	})
}

func TestDecodeJSON(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.NoError(t, DecodeJSON([]byte(`{"name":"test"}`), &dest))
		test.EqOp(t, "test", dest.Name)
	})

	T.Run("with invalid data", func(t *testing.T) {
		t.Parallel()

		var dest example
		test.Error(t, DecodeJSON([]byte(`{invalid`), &dest))
	})
}

func TestMustDecodeJSON(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var dest example
		MustDecodeJSON([]byte(`{"name":"test"}`), &dest)
		test.EqOp(t, "test", dest.Name)
	})
}

func TestMustJSONIntoReader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		reader := MustJSONIntoReader(&example{Name: t.Name()})
		must.NotNil(t, reader)

		data, err := io.ReadAll(reader)
		must.NoError(t, err)
		test.SliceNotEmpty(t, data)
	})
}
