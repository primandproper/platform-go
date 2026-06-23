package memory

import (
	"testing"
	"time"

	"github.com/shoenig/test/must"
)

func BenchmarkLocker_AcquireRelease(b *testing.B) {
	l, err := NewLocker(nil, nil, nil)
	must.NoError(b, err)

	ctx := b.Context()
	for b.Loop() {
		lock, acqErr := l.Acquire(ctx, "bench-key", time.Minute)
		must.NoError(b, acqErr)
		must.NoError(b, lock.Release(ctx))
	}
}
