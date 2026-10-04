package hookroster

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/database"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type storeEntry struct {
	store  reflect.Type
	hooks  reflect.Type
	exempt map[string]string
}

// stores are the packages whose Store runs Hooks, keyed by their directory.
var stores = map[string]storeEntry{
	"authentication/passwordreset": {store: reflect.TypeFor[passwordreset.Store](), hooks: reflect.TypeFor[passwordreset.Hooks]()},
	"billing":                      {store: reflect.TypeFor[billing.Store](), hooks: reflect.TypeFor[billing.Hooks]()},
	"comments":                     {store: reflect.TypeFor[comments.Store](), hooks: reflect.TypeFor[comments.Hooks]()},
	"issuereports":                 {store: reflect.TypeFor[issuereports.Store](), hooks: reflect.TypeFor[issuereports.Hooks]()},
	"mediaregistry":                {store: reflect.TypeFor[mediaregistry.Store](), hooks: reflect.TypeFor[mediaregistry.Hooks]()},
	"notifications":                {store: reflect.TypeFor[notifications.Store](), hooks: reflect.TypeFor[notifications.Hooks]()},
	"settings":                     {store: reflect.TypeFor[settings.Store](), hooks: reflect.TypeFor[settings.Hooks]()},
	"waitlists":                    {store: reflect.TypeFor[waitlists.Store](), hooks: reflect.TypeFor[waitlists.Hooks]()},
	"webhooks": {
		store: reflect.TypeFor[webhooks.Store](),
		hooks: reflect.TypeFor[webhooks.Hooks](),
		exempt: map[string]string{
			"Enqueue": "its only caller is Dispatcher.Dispatch, and a companion for a fan-out is written beside that call, which already holds every field of the delivery",
		},
	},
}

// services are the packages whose Hooks a Service runs rather than its Store.
// A Service method owns its transaction and takes no Tx, so the pairing above
// has nothing to read there; each is checked by its own package's tests.
var services = map[string]string{
	"authentication/oauth2clients": "run by Service, around the store writes it composes",
	"authentication/passkeys":      "run by Service, around a ceremony's writes",
	"authentication/signin":        "run by Service, around a sign-in's writes",
	"identity":                     "run by Service, around the store writes it composes",
}

func TestEveryStoreWriteHasItsHook(T *testing.T) {
	T.Parallel()

	tx := reflect.TypeFor[database.Tx]()

	for dir, entry := range stores {
		T.Run(dir, func(t *testing.T) {
			t.Parallel()

			writes := map[string]bool{}
			for m := range entry.store.Methods() {
				if m.Type.NumIn() < 2 || m.Type.In(1) != tx {
					continue
				}
				writes[m.Name] = true

				if why, ok := entry.exempt[m.Name]; ok {
					test.NotEq(t, "", why, test.Sprintf("exemption for %s gives no reason", m.Name))
					_, hooked := entry.hooks.MethodByName("After" + m.Name)
					test.False(t, hooked, test.Sprintf("%s is exempt but has a hook; drop the exemption", m.Name))
					continue
				}

				_, ok := entry.hooks.MethodByName("After" + m.Name)
				test.True(t, ok, test.Sprintf("Store.%s takes the caller's Tx and Hooks has no After%s", m.Name, m.Name))
			}

			for name := range entry.exempt {
				test.True(t, writes[name], test.Sprintf("exemption for %s names no write on Store", name))
			}

			for m := range entry.hooks.Methods() {
				write, ok := strings.CutPrefix(m.Name, "After")
				test.True(t, ok && writes[write], test.Sprintf("Hooks.%s names no write on Store", m.Name))
			}
		})
	}
}

var declaresHooks = regexp.MustCompile(`(?m)^type Hooks interface`)

func TestEveryHooksPackageIsRostered(T *testing.T) {
	T.Parallel()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(T, err)

	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if declaresHooks.Match(src) {
			rel, relErr := filepath.Rel(root, filepath.Dir(path))
			if relErr != nil {
				return relErr
			}
			found[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	must.NoError(T, err)

	for dir := range found {
		_, isStore := stores[dir]
		_, isService := services[dir]
		test.True(T, isStore || isService, test.Sprintf("%s declares Hooks and is on neither roster", dir))
	}
	for dir := range stores {
		test.True(T, found[dir], test.Sprintf("stores names %s, which declares no Hooks", dir))
	}
	for dir := range services {
		test.True(T, found[dir], test.Sprintf("services names %s, which declares no Hooks", dir))
	}
}
