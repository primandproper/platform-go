package pagedrpc

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestEveryPagedRPCHasARequest keeps the builders table honest in both
// directions. A paged read whose request carries a field beyond its filter and
// has no builder would be asserted against with that field empty, and refused
// for it rather than for the filter — so it fails here. A builder for a read
// that is not paged, or no longer exists, is a stale entry, and fails here too.
func TestEveryPagedRPCHasARequest(t *testing.T) {
	t.Parallel()

	all := All()
	must.SliceNotEmpty(t, all, must.Sprint("no paged reads found, so nothing would be asserted"))

	table := builders()
	paged := map[string]struct{}{}

	for _, rpc := range all {
		paged[rpc.FullName] = struct{}{}

		fields := rpc.Method.Input().Fields()
		must.NotNil(t, fields.ByName(filterField),
			must.Sprintf("%s carries a QueryFilter under a name other than %q", rpc.FullName, filterField))

		if fields.Len() == 1 {
			continue
		}

		test.MapContainsKey(t, table, rpc.FullName,
			test.Sprintf("%s takes more than a filter and has no builder, so it would be asserted against with its other fields empty", rpc.FullName))
	}

	for name := range table {
		test.MapContainsKey(t, paged, name, test.Sprintf("builder for %s, which is not a paged read", name))
	}
}
