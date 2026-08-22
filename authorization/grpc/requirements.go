package grpc

import (
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrEmptyMethod indicates a requirement was declared for an empty method name.
	ErrEmptyMethod = errors.New("empty method name")
	// ErrDuplicateMethod indicates the same method was declared more than once.
	ErrDuplicateMethod = errors.New("method declared more than once")
	// ErrNoPermissionsRequired indicates Require was called with no permissions,
	// which would authorize every caller for that method.
	ErrNoPermissionsRequired = errors.New("method required with no permissions")
	// ErrEmptyPermission indicates a requirement listed an empty permission.
	ErrEmptyPermission = errors.New("empty permission required")
)

// Requirements is the frozen table of what each RPC method demands.
//
// It is immutable once built, which is why the interceptors need no lock. A
// mutable table guarded by a mutex costs a lock acquisition on every RPC to
// protect a map that is never written after startup.
type Requirements struct {
	byMethod map[string][]authorization.Permission
	public   map[string]struct{}
}

// RequirementsBuilder accumulates method requirements and validates them as a
// whole.
type RequirementsBuilder struct {
	byMethod map[string][]authorization.Permission
	public   map[string]struct{}
	declared map[string]int
	errs     []error
}

// NewRequirements returns a builder for a Requirements table.
func NewRequirements() *RequirementsBuilder {
	return &RequirementsBuilder{
		byMethod: map[string][]authorization.Permission{},
		public:   map[string]struct{}{},
		declared: map[string]int{},
	}
}

// Require declares that fullMethod demands every permission in perms.
//
// Requiring zero permissions is an error rather than a way to say "any
// authenticated caller" — it reads as a requirement while behaving as an
// allow, and that gap is where an authorization hole hides. Say Public
// instead, which means the same thing and looks like it.
func (b *RequirementsBuilder) Require(fullMethod string, perms ...authorization.Permission) *RequirementsBuilder {
	b.declared[fullMethod]++

	switch {
	case fullMethod == "":
		b.errs = append(b.errs, ErrEmptyMethod)
	case len(perms) == 0:
		b.errs = append(b.errs, errors.Wrapf(ErrNoPermissionsRequired, "method %q", fullMethod))
	}

	for _, p := range perms {
		if p == "" {
			b.errs = append(b.errs, errors.Wrapf(ErrEmptyPermission, "method %q", fullMethod))
		}
	}

	if fullMethod != "" && len(perms) > 0 {
		b.byMethod[fullMethod] = slices.Clone(perms)
	}

	return b
}

// RequireAll declares requirements from a map, which is the shape a service
// package naturally exports for its own methods. Several such maps merge into
// one table, and a method declared by two of them is reported as a duplicate
// rather than silently taking whichever was applied last.
func (b *RequirementsBuilder) RequireAll(m map[string][]authorization.Permission) *RequirementsBuilder {
	for _, method := range slices.Sorted(maps.Keys(m)) {
		b.Require(method, m[method]...)
	}

	return b
}

// Public declares that fullMethod requires no authorization.
//
// Being public is a declaration, never an omission. An undeclared method is
// denied, so forgetting to register a route fails closed and loudly, while
// forgetting to mark one public fails closed and obviously.
func (b *RequirementsBuilder) Public(fullMethod string) *RequirementsBuilder {
	b.declared[fullMethod]++

	if fullMethod == "" {
		b.errs = append(b.errs, ErrEmptyMethod)

		return b
	}

	b.public[fullMethod] = struct{}{}

	return b
}

// Build validates the accumulated declarations and freezes them.
//
// It reports every problem it found rather than the first, because a table
// assembled from a dozen service packages usually has more than one, and
// fixing them one restart at a time is miserable.
func (b *RequirementsBuilder) Build() (*Requirements, error) {
	for _, method := range slices.Sorted(maps.Keys(b.declared)) {
		if b.declared[method] > 1 {
			b.errs = append(b.errs, errors.Wrapf(ErrDuplicateMethod, "method %q", method))
		}
	}

	if err := errors.Join(b.errs...); err != nil {
		return nil, err
	}

	return &Requirements{
		byMethod: maps.Clone(b.byMethod),
		public:   maps.Clone(b.public),
	}, nil
}

// lookup reports what fullMethod requires: the permissions, whether the method
// is public, and whether it was declared at all.
func (r *Requirements) lookup(fullMethod string) (perms []authorization.Permission, public, declared bool) {
	if _, ok := r.public[fullMethod]; ok {
		return nil, true, true
	}
	perms, ok := r.byMethod[fullMethod]

	return perms, false, ok
}

// Methods returns every declared method name, sorted. It exists so consumers
// can assert their table covers every method their server registers — the
// check that turns "we remembered to declare everything" from a convention
// into a test.
func (r *Requirements) Methods() []string {
	out := make([]string, 0, len(r.byMethod)+len(r.public))
	out = append(out, slices.Collect(maps.Keys(r.byMethod))...)
	out = append(out, slices.Collect(maps.Keys(r.public))...)
	slices.Sort(out)

	return out
}
