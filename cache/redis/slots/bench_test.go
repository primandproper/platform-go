package slots_test

import (
	"testing"

	"github.com/primandproper/platform-go/cache/redis/slots"
)

func BenchmarkSlotForKey(b *testing.B) {
	cases := map[string]string{
		"plain":   "user1000",
		"hashtag": "{user1000}.following",
	}
	for name, key := range cases {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				slotSink = slots.SlotForKey(key)
			}
		})
	}
}

var slotSink uint16
