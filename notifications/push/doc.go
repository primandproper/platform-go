/*
Package push is the fan-out: one announcement, every handset the people named
have registered, and the dead tokens pruned on the way.

It is three calls a consumer otherwise writes for itself, and the third is the
one that gets written wrong. [notifications.Registry.ListDevicesByPrincipals]
resolves recipients to tokens in one query,
[github.com/primandproper/primitives-go/v2/notifications/mobile.PushNotificationSender]
sends to one token at a time, and
[notifications.Registry.InvalidateDeviceToken] removes a token the provider has
permanently rejected. Both halves have shipped since this package's parent did;
what had not shipped was the loop that joins them.

# The bad-token match is a sentinel, not a string

A provider does not report a dead token as a distinct kind of failure. APNs
answers a push with Unregistered or BadDeviceToken and FCM with UNREGISTERED,
each inside an ordinary error, and the classification that matters —
"retry this" against "delete this row" — is the senders' to make.
notifications/mobile makes it: a permanent rejection comes back wrapped in
mobile.ErrTokenInvalid, and errors.Is is how this package asks.

The fan-out written against the message text instead is the one this replaces,
and its failure is silent in the direction nobody watches. A provider rewords
its answer, the match stops matching, and what is left is a registry that still
lists the token, a handset that receives nothing, and a send that fails
identically forever — with no log line saying anything changed, because from
here nothing did. A provider adapter that surfaces no typed sentinel is that
adapter's gap to close, and this package deliberately has no fallback to paper
over one.

# One dead handset does not stop the announcement

Every device the query resolved is sent to, whatever the device before it
answered. A push is a fan-out precisely because its recipients are independent:
one person's uninstalled app is not a reason the other twenty-nine hear nothing.
So a send that fails is recorded against its own device, the loop continues, and
what comes back is a [Result] describing every delivery attempted together with
the failures joined into one error — which is the shape retention.Sweeper and
metering's flush already answer partial work with.

A caller that checks only the error still learns that something did not arrive.
A caller that reads the [Result] learns which handsets, whose they were, and
whether the dead ones were pruned.

Sends run in order, one at a time. Concurrency here would be a decision about
somebody else's provider rate limits taken inside a library, and the caller who
wants it has the device list — [notifications.Registry.ListDevicesByPrincipals]
is exported, and this package is the loop over it rather than the only way to
write one.

# Wiring, and what it does not replace

	fanout, err := push.NewFanout(store, sender)
	// ...
	result, err := fanout.Push(ctx, client.Reader(), scope, principals,
		mobile.PushMessage{Title: "Your order shipped", Body: "..."})

The sender may also carry the registry itself —
mobile.WithTokenInvalidator(store) — and a deployment doing both is not doing it
twice in any sense that costs anything: [notifications.Registry.InvalidateDeviceToken]
is idempotent, and a token the sender already removed is removed again to no
effect. The two are not alternatives either. The sender's hook prunes for every
caller of that sender, this prunes for every caller of this fan-out, and a
deployment whose sender is not one of this module's has only the second.

# What this does not do

It does not file inbox rows. Telling somebody something durably is
[notifications.Inbox.CreateNotification], it belongs in the transaction that
wrote whatever the notification is about, and a push is the ephemeral copy that
arrives on a lock screen — see the parent package for why those two are
deliberately not one call. A consumer that wants both writes the inbox rows in
its transaction and pushes after it commits, which is the order that does not
announce an order that was refused.

It also holds no executor. The read takes the caller's, like every other read in
this module, so a fan-out run inside the transaction that registered a handset
moments earlier sees it.
*/
package push
