package push

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// fanoutName scopes this package's spans, logger and instruments.
const fanoutName = "notifications_push_fanout"

// The keys this package attaches to spans and to its instruments.
//
// The scope is the one fact the registry also names, so it is spelled where the
// registry spells it rather than again here — a second copy of an attribute key
// is a copy that can drift, and a trace whose two halves label the same tenant
// differently is a trace nobody can join.
const (
	scopeKey       = notifications.ScopeAttributeKey
	principalsKey  = "notifications.push.principals"
	devicesKey     = "notifications.push.devices"
	sentKey        = "notifications.push.sent"
	failedKey      = "notifications.push.failed"
	invalidatedKey = "notifications.push.invalidated"
	platformKey    = "notifications.push.platform"
)

// The sentinels this package returns, both at construction.
//
// There is no sentinel for a nil executor, and that absence is deliberate: the
// read this makes is the registry's, so a nil one is
// notifications.ErrNilExecutor arriving from the call that would have used it.
// A second sentinel for the same refusal would be a second thing for a caller
// to match on.
var (
	// ErrNilRegistry indicates a nil notifications.Registry. The fan-out resolves
	// recipients through it and prunes through it, so there is nothing it could
	// do without one.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications device registry for the push fan-out")

	// ErrNilSender indicates a nil mobile.PushNotificationSender.
	//
	// It is refused rather than defaulted to the noop sender, which is the
	// module's reading of an unnamed implementation: a fan-out that reported
	// every push as delivered and sent none would be the failure this package
	// exists to end, wearing a successful result. A deployment that genuinely
	// wants no pushes names notifications/mobile/noop.
	ErrNilSender = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil push notification sender for the fan-out")
)

// Delivery is one device's share of a fan-out: the registration that was pushed
// to, and what came of it.
type Delivery struct {
	// Err is what the sender answered, and nil for a push that was accepted.
	//
	// It carries the provider's own words, because mobile.ErrTokenInvalid is
	// wrapped around a provider's error rather than substituted for it — so
	// errors.Is answers the classification and the message still says what APNs
	// or FCM actually returned.
	Err error

	// Device is the registration this push was addressed to, as the registry
	// held it. It carries the principal and the device id a caller needs to say
	// which handset this was, and the token, which is what the row is.
	Device *notifications.Device

	// Invalidated reports whether the provider called the token permanently dead
	// and the prune that followed reported success.
	//
	// It is about the prune rather than about a row: [notifications.Registry.InvalidateDeviceToken]
	// is idempotent, so a token something else had already removed is reported
	// here exactly like one this call removed. What the flag separates is the
	// dead token that was acted on from the dead token whose prune failed —
	// which is an Err matching mobile.ErrTokenInvalid with this still false.
	Invalidated bool
}

// Result is what one fan-out did, in the order the registry returned the
// devices.
//
// It is returned alongside the error rather than instead of it. A fan-out that
// reached twenty-nine handsets and failed on one has done both things, and a
// caller told only the second has no way to learn the first — see
// [Fanout.Push].
type Result struct {
	// Deliveries holds one entry per device the fan-out resolved, whether the
	// push was accepted or refused. A fan-out that resolved no devices holds
	// none, which is the answer for an announcement addressed to people who have
	// registered no handsets.
	Deliveries []Delivery

	// Sent is how many pushes the sender accepted.
	Sent int

	// Failed is how many it did not, and is the length of the joined error
	// [Fanout.Push] returns beside this.
	Failed int

	// Invalidated is how many dead tokens were pruned — a subset of Failed,
	// never a series beside it, because a token is classified dead by a send
	// that failed.
	Invalidated int
}

// Fanout sends one message to every handset a set of people have registered.
//
// It holds no executor and no database.Client. The read it makes runs on the
// executor its caller supplies, and the prune runs wherever the registry runs
// its own — which for notifications.SQLStore is the connection that store was
// built with, because a provider's verdict joins nobody's transaction.
type Fanout struct {
	registry notifications.Registry
	sender   mobile.PushNotificationSender
	o11y     observability.Observer

	// instruments count the sends rather than the fan-outs, because a push to
	// one token is the unit that succeeds or fails: a request count of "how many
	// announcements went out" leaves an error rate that means nothing, since one
	// announcement covers one handset or three hundred.
	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	// Read o11y.Logger() for the logger this fan-out actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewFanout builds the fan-out over a device registry and a push sender. Both
// are required.
//
// The registry is the interface rather than *notifications.SQLStore, so a
// consumer whose devices are not this module's schema still gets the loop; the
// sender is mobile's interface, so the provider behind it is the deployment's
// choice and the bad-token classification is that provider adapter's.
//
// Observability is optional and defaults to nothing: an unconfigured fan-out
// logs to a noop logger, traces to a noop provider and records to noop
// instruments.
func NewFanout(
	registry notifications.Registry,
	sender mobile.PushNotificationSender,
	opts ...Option,
) (*Fanout, error) {
	if registry == nil {
		return nil, ErrNilRegistry
	}

	if sender == nil {
		return nil, ErrNilSender
	}

	f := &Fanout{registry: registry, sender: sender}

	for _, opt := range opts {
		if opt != nil {
			opt(f)
		}
	}

	f.o11y = observability.NewObserver(fanoutName, f.logger, f.tracerProvider)

	instruments, err := metrics.NewOperationSet(f.metricsProvider, fanoutName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating the notifications push fan-out instruments")
	}

	f.instruments = instruments

	return f, nil
}

// Push resolves the principals to the handsets they have registered in this
// scope, sends the message to each, and prunes the tokens the provider calls
// permanently dead.
//
// Pass Client.Reader(). The executor is the module's read shape and a
// transaction satisfies it, so a fan-out handed one does reach a handset
// registered moments earlier in the same request — but the read is the first
// thing this call does and every provider round trip happens after it, inside
// the same scope. A transaction handed to a fan-out over thirty handsets is a
// transaction held open across thirty sequential calls to somebody else's
// network, and the sends are the part with no bound on how long they take.
// That is the long hold this module's other machinery is written to avoid,
// arrived at through an affordance that reads like a convenience.
//
// What that convenience is for is narrow and worth naming, because the wider
// type is deliberate rather than accidental: a caller that has just written a
// registration and wants to push to it, with a device set it knows is small.
// Anything else resolves inside its transaction through
// [notifications.Registry.ListDevicesByPrincipals], commits, and pushes after —
// which is the order the package documentation already gives for inbox rows,
// for the same reason. It does not announce something that was refused, and it
// does not hold a table open while an announcement is delivered.
//
// An empty set of principals resolves to no devices without a query — see
// [notifications.Registry.ListDevicesByPrincipals] — and answers with an empty
// [Result] and no error.
//
// A resolve that fails is the one failure that stops everything, and it answers
// with a nil Result: nothing was sent, and there is nothing to describe. After
// that every device is attempted whatever the ones before it answered, and the
// answer is both values — the Result describing every delivery, and the failures
// joined into one error. A caller that checks only the error still learns that
// something did not arrive; a caller that reads the Result learns which handsets
// and whose.
func (f *Fanout) Push(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principals []string,
	msg mobile.PushMessage,
) (*Result, error) {
	ctx, op := f.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(principalsKey, len(principals)),
	)
	defer op.End()

	devices, err := f.registry.ListDevicesByPrincipals(ctx, q, scope, principals)
	if err != nil {
		return nil, op.Error(err, "resolving the handsets to push to")
	}

	result := &Result{Deliveries: make([]Delivery, 0, len(devices))}

	var errs []error

	for _, device := range devices {
		delivery := f.deliver(ctx, op, device, msg)

		result.Deliveries = append(result.Deliveries, delivery)

		if delivery.Err != nil {
			result.Failed++
			errs = append(errs, delivery.Err)
		} else {
			result.Sent++
		}

		if delivery.Invalidated {
			result.Invalidated++
		}
	}

	op.SpanOnly(devicesKey, len(devices)).
		SpanOnly(sentKey, result.Sent).
		SpanOnly(failedKey, result.Failed).
		SpanOnly(invalidatedKey, result.Invalidated)

	if len(errs) > 0 {
		return result, op.Error(platformerrors.Join(errs...), "pushing to %d handsets", len(devices))
	}

	return result, nil
}

// deliver sends to one handset and acts on the provider's verdict.
//
// A failed send is not acknowledged here. Every one of them is returned, joined,
// by the call above, so a line per device would report the same failures twice
// against the same operation — and the sender has already logged and spanned the
// round trip that produced each one.
func (f *Fanout) deliver(
	ctx context.Context,
	op observability.Operation,
	device *notifications.Device,
	msg mobile.PushMessage,
) Delivery {
	attr := platformAttr(device.Platform)

	f.instruments.Attempt(ctx, attr)

	defer op.Time(ctx, nil, f.instruments.Latency, attr)()

	err := f.sender.SendPush(ctx, device.Platform.String(), device.Token, msg)
	if err == nil {
		return Delivery{Device: device}
	}

	f.instruments.Failed(ctx, attr)

	delivery := Delivery{Device: device, Err: err}

	// The typed sentinel, and nothing about the provider's wording. This is the
	// whole of the difference between a registry that sheds dead tokens and one
	// that stops shedding them the day a provider rewrites a message.
	if !errors.Is(err, mobile.ErrTokenInvalid) {
		return delivery
	}

	// A failed prune is acknowledged and swallowed, which is the reading
	// mobile.WithTokenInvalidator's own hook takes. The push has already failed
	// and the token is already known dead; replacing that diagnosis with "the
	// database was busy" is the one outcome worse than either. The row survives,
	// the next fan-out classifies it again, and the log says the prune did not
	// take.
	if invalidateErr := f.registry.InvalidateDeviceToken(ctx, device.Platform.String(), device.Token); invalidateErr != nil {
		op.Acknowledge(invalidateErr, "pruning the device token the provider rejected")

		return delivery
	}

	delivery.Invalidated = true

	return delivery
}

// platformAttr labels one send's instruments, so the trio shares a dimension
// rather than each declaring one.
//
// The platform and nothing finer. A token is the identifier a provider
// addresses somebody's handset with, and an instrument dimensioned on one is an
// unbounded cardinality that also writes the token into every metrics backend it
// reaches.
func platformAttr(p notifications.Platform) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(platformKey, p.String()))
}
