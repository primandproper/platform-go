/*
Package mail is the queued transport for the mail this module's services owe:
one QueuedMailer that satisfies every Mailer seam here by enqueuing the mail on
the outbox, and one Drainer that takes it off the topic, renders it through the
consumer's Renderer and sends it through an email.Emailer.

Six seams hand mail to the consumer — identity.InvitationMailer,
signin.VerificationMailer, signin.HandleReminderMailer, signin.MagicLinkMailer,
passwordreset.Mailer and waitlistsgrpc.ConfirmationMailer — and the wording is
the consumer's, by the ruling dataprivacy.Notifier states. What the module left
out was the transport those six share. A deployment that runs an outbox does
not send mail from the request, so every such deployment wrote, for each seam,
the same adapter and the same drain. This is that adapter and that drain,
written once:

	mailer, _ := mail.NewQueuedMailer(client, outboxWriter, "outbound-mail")
	resets, _ := passwordreset.NewService(client, store, directory, authn, mailer)

	drainer, _ := mail.NewDrainer(emailer, renderer)
	pool, _ := jobs.NewPool(ctx, &jobs.PoolConfig{Topic: "outbound-mail"}, consumers, drainer.Handle)

# What the guarantee is

Every one of the six seams is called after the transaction that wrote its row
has committed, and none is handed that transaction, so this mailer cannot
enqueue on it. Each Send opens a transaction of its own on the client it was
built with, enqueues one message, and commits. That is the narrowest
transaction there is, and what it buys is stated rather than implied:

  - A Send that returns nil has made the mail durable. A mail provider that is
    down when the request arrives costs a retry on the drain, not a failed
    request — which is the reason to queue at all.
  - A Send that returns an error enqueued nothing, and the service that called
    it reports the failure exactly as it reports a synchronous send's: the row
    is committed and the caller is told nobody was mailed.
  - A process that dies between the service's commit and this commit leaves a
    row and no mail, and nobody is told. That window is the one a synchronous
    emailer has too, between the same commit and the provider's acceptance;
    queuing narrows it to one local write and does not close it. Closing it
    would mean the seams handing their transaction over, which every one of
    them refuses on purpose: a mail enqueued inside the write is a mail for a
    row that may still roll back, and once drained it cannot be taken back.

Registration's verification mail is not on this list, because it is not on
any seam. A registration's token reaches the consumer on the
identity.user.registered event, inside the registration's own transaction,
and a deployment that mails from that event is mailing from the event's
transport.

# The secret rides on the message

A message carries the one credential its mail exists to deliver — the
invitation's token, the verification token, the magic link's or the reset's
secret, the waitlist's two links — because the drain is the one party that
renders it into a URL, and every seam's value hides it from encoding/json on
purpose. The wire form names it explicitly instead, and the Drainer hands the
Renderer the same value the synchronous seam would have been given, secret
restored.

That makes the topic a credential store for as long as a message sits on it,
and three things follow:

  - The topic is the drain's alone. It is never the topic a webhooks.Emitter
    publishes on, nor one any other subscriber reads: a token on a subscribed
    topic is a second copy of the secret that outlives the mail it was for.
  - The outbox.Writer it is handed should register no side effect that copies
    what it enqueues anywhere else, because a side effect sees every message,
    this one included.
  - Nothing here logs a payload, and a Renderer should not either.

# The recipient is personal data on a queue

Every message carries the address it is going to, and dataprivacy's erasure
does not reach a queue. The retention is therefore the deployment's to keep
short, in the three places a message can rest: the outbox row, kept for
outbox.RelayConfig.Retention once published (a day by default); the broker's
own retention on the topic; and wherever the jobs.Pool's dead letters go. A
link's own lifetime is the natural bound for all three — a mail whose link has
expired is not worth delivering, and an address kept past that is kept for
nothing.

# Test mail

ContextWithTestID puts a correlation identifier on a request's context, and a
Send made under it carries it on the message to Mail.TestID, for a
deployment's deliverability canary to find its own mail by. The identifier is
the consumer's to render into the message — a header, a tag, a subject
suffix — since email.OutboundEmailMessage has nowhere to put it and the
Renderer is where the message is built.

# Which tier this is

The domain's. It owns no table, and it is here rather than beside the email
primitive because what it encodes is this module's six mails; an application
with no users has none of them to send.
*/
package mail
