package slots

import (
	"math/rand/v2"
	"testing"

	"github.com/shoenig/test/must"
)

// crc16Reference is a deliberately naive bit-by-bit CRC16-CCITT (XMODEM)
// implementation used as an independent oracle for the table-driven
// crc16sum. If the two ever disagree, the table is wrong.
func crc16Reference(s string) uint16 {
	var crc uint16
	for i := 0; i < len(s); i++ {
		crc ^= uint16(s[i]) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func TestSlot_invariants(T *testing.T) {
	T.Parallel()

	T.Run("empty input hashes to zero", func(t *testing.T) {
		t.Parallel()

		must.EqOp(t, uint16(0), Slot(""))
	})

	T.Run("output is always within the slot range", func(t *testing.T) {
		t.Parallel()

		r := rand.New(rand.NewPCG(1, 2))
		for range 256 {
			buf := make([]byte, 1+r.IntN(64))
			for j := range buf {
				buf[j] = byte(r.IntN(256))
			}
			must.Less(t, uint16(SlotCount), Slot(string(buf)))
		}
	})

	T.Run("identical input produces identical slot", func(t *testing.T) {
		t.Parallel()

		must.EqOp(t, Slot("key:version42"), Slot("key:version42"))
	})
}

func TestSlot_matchesReferenceImpl(T *testing.T) {
	T.Parallel()

	r := rand.New(rand.NewPCG(0xC0FFEE, 0xDEADBEEF))
	for i := range 1000 {
		buf := make([]byte, 1+r.IntN(32))
		for j := range buf {
			buf[j] = byte(r.IntN(256))
		}
		s := string(buf)

		want := crc16Reference(s) % SlotCount
		got := Slot(s)
		must.EqOp(T, want, got, must.Sprintf("iter %d input %q", i, s))
	}
}

func TestSlotForKey(T *testing.T) {
	T.Parallel()

	cases := []struct {
		name string
		key  string
		want uint16
	}{
		{
			name: "no hashtag falls through to whole key",
			key:  "foo",
			want: Slot("foo"),
		},
		{
			name: "hashtag extracts inner content",
			key:  "{user1000}.following",
			want: Slot("user1000"),
		},
		{
			name: "two keys share a slot when hashtag matches",
			key:  "{user1000}.followers",
			want: Slot("user1000"),
		},
		{
			name: "empty hashtag falls through to whole key",
			key:  "{}foo",
			want: Slot("{}foo"),
		},
		{
			name: "open brace without close falls through",
			key:  "{foo",
			want: Slot("{foo"),
		},
		{
			name: "only first hashtag is used",
			key:  "{first}{second}",
			want: Slot("first"),
		},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			must.EqOp(t, tc.want, SlotForKey(tc.key))
		})
	}
}
