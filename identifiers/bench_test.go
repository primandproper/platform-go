package identifiers_test

import (
	"testing"

	"github.com/primandproper/platform/identifiers"
)

func BenchmarkNew(b *testing.B) {
	for b.Loop() {
		strSink = identifiers.New()
	}
}

func BenchmarkValidate(b *testing.B) {
	id := identifiers.New()
	for b.Loop() {
		errSink = identifiers.Validate(id)
	}
}

var (
	strSink string
	errSink error
)
