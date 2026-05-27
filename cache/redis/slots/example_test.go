package slots_test

import (
	"fmt"
	"sort"

	"github.com/primandproper/platform/cache/redis/slots"
)

// Builds a KeyGenerator for a 3-node cluster (one slot per node) and shows
// that the same seed always returns the same hashtag — so a caller that does
// gen.SlottedKey(userID) + userID can read back what they wrote.
func ExampleNewKeyGenerator() {
	gen, err := slots.NewKeyGenerator("example:v1:", "", slots.FromClusterConfig(3, 1))
	if err != nil {
		panic(err)
	}

	tags := gen.Hashtags()
	sort.Strings(tags)
	fmt.Println("precomputed:", tags)

	const user = "alice"
	first := gen.SlottedKey(user) + user
	second := gen.SlottedKey(user) + user
	fmt.Printf("%s key: %s\n", user, first)
	fmt.Printf("stable across calls: %t\n", first == second)

	// Output:
	// precomputed: [{example:v1:0} {example:v1:1} {example:v1:2}]
	// alice key: {example:v1:1}alice
	// stable across calls: true
}
