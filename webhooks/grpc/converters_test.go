package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestConvertersTolerateNil is what a page containing a nil entry does to a
// consumer composing these into a larger response.
func TestConvertersTolerateNil(T *testing.T) {
	T.Parallel()

	test.Nil(T, webhooksgrpc.EndpointToProto(nil))
	test.Nil(T, webhooksgrpc.SubscriptionToProto(nil))
	test.Nil(T, webhooksgrpc.AttemptToProto(nil))

	// The plural forms answer an empty slice rather than nil, so a response's
	// repeated field is empty rather than absent — which is the same JSON either
	// way and a different Go value for a consumer ranging over it.
	test.SliceEmpty(T, webhooksgrpc.EndpointsToProto(nil))
	test.SliceEmpty(T, webhooksgrpc.SubscriptionsToProto(nil))
	test.SliceEmpty(T, webhooksgrpc.AttemptsToProto(nil))
}

// TestTheNullableTimesStayUnset is the difference between "nobody has updated
// this" and "it was updated in 1970".
func TestTheNullableTimesStayUnset(T *testing.T) {
	T.Parallel()

	created := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	endpoint := webhooksgrpc.EndpointToProto(&webhooks.Endpoint{CreatedAt: created})
	must.NotNil(T, endpoint)
	test.EqOp(T, created, endpoint.GetCreatedAt().AsTime())
	test.Nil(T, endpoint.GetLastUpdatedAt())
	test.Nil(T, endpoint.GetArchivedAt())

	updated := created.Add(time.Hour)
	archived := created.Add(2 * time.Hour)

	endpoint = webhooksgrpc.EndpointToProto(&webhooks.Endpoint{
		CreatedAt:     created,
		LastUpdatedAt: &updated,
		ArchivedAt:    &archived,
	})
	test.EqOp(T, updated, endpoint.GetLastUpdatedAt().AsTime())
	test.EqOp(T, archived, endpoint.GetArchivedAt().AsTime())

	subscription := webhooksgrpc.SubscriptionToProto(&webhooks.Subscription{CreatedAt: created})
	test.Nil(T, subscription.GetLastUpdatedAt())
	test.Nil(T, subscription.GetArchivedAt())
}

// TestCreatedByIsAnIdentifierAndNotProse is why the converter reads Owner
// rather than String.
//
// tenancy.Scope.String is for a log field: it renders the scope naming nobody as
// "<unset>" and the global one as "<global>", both of which a client would show
// to somebody as the name of the person who registered an endpoint.
func TestCreatedByIsAnIdentifierAndNotProse(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		want  string
		scope tenancy.Scope
	}{
		"a person":     {scope: tenancy.Of("user_1"), want: "user_1"},
		"nobody":       {scope: tenancy.Scope{}, want: ""},
		"the operator": {scope: tenancy.Global(), want: ""},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			out := webhooksgrpc.EndpointToProto(&webhooks.Endpoint{CreatedBy: tc.scope})
			test.EqOp(t, tc.want, out.GetCreatedBy())
		})
	}
}

// TestAnEndpointRendersItsLiveSubscriptions covers the one value slice in the
// module, which the unexported half of the subscription converter reads.
func TestAnEndpointRendersItsLiveSubscriptions(T *testing.T) {
	T.Parallel()

	out := webhooksgrpc.EndpointToProto(&webhooks.Endpoint{
		Subscriptions: []webhooks.Subscription{
			{ID: "sub_1", EventType: "order.created"},
			{ID: "sub_2", EventType: "order.shipped"},
		},
	})

	must.SliceLen(T, 2, out.GetSubscriptions())
	test.EqOp(T, "order.created", out.GetSubscriptions()[0].GetEventType())
	test.EqOp(T, "order.shipped", out.GetSubscriptions()[1].GetEventType())
}

// TestAnAttemptKeepsItsPrecision is the one field that changes units on the way
// out: a Go duration is nanoseconds and the store rounds to milliseconds, and
// the proto carries whatever the store gave back.
func TestAnAttemptKeepsItsPrecision(T *testing.T) {
	T.Parallel()

	out := webhooksgrpc.AttemptToProto(&webhooks.Attempt{
		Duration:     1500 * time.Millisecond,
		StatusCode:   503,
		AttemptCount: 4,
	})

	test.EqOp(T, 1500*time.Millisecond, out.GetDuration().AsDuration())
	test.EqOp(T, int32(503), out.GetStatusCode())
	test.EqOp(T, int32(4), out.GetAttemptCount())
}
