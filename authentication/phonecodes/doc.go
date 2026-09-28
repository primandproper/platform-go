/*
Package phonecodes is where a texted code lives: a short numeric credential sent
to a phone number, stored as a digest, spent once, and dead after a handful of
wrong guesses — for a person who is not a user.

identity's user is the only person the rest of this module can name. An
application whose clients are a phone number and a name, never a password, has
exactly one credential such a person can prove: that they can read a text sent
to that number. This package is the table behind "we texted you a code, type it
in", and it is the whole of that flow's storage.

# The shape, and where it came from

It is authentication/signin/magiclinks' shape with the two things a phone code is
not taken out. Issue writes a digest and hands the plaintext back once; Redeem
spends it in the caller's transaction; RevokeForSubject ends every live one; a
sweeper reaps the expired; three dialects, a mock, a privacy adapter.

What a sign-in link stores is thirty-two random bytes bound to a user and an
email address. What this stores is six digits bound to an opaque subject and a
phone number:

  - The subject is the consumer's. Issue takes a subject identifier and an E.164
    number, not a user id, and there is no foreign key: the person is not a
    user, and binding the table to identity would make it useless to the
    application that needs it. What the subject is — a contact, a household
    member — is the consumer's to say.
  - A code has a million values where a link secret has 2^256, so a code
    carries an attempt count and a limit, and a link does not need one.

# The attempt limit

A wrong code against a live one is counted, and a code at its limit is dead
whatever it is presented with. The store's limit is at most MaxAttemptsCeiling,
and a request may tighten it for one code but never loosen it. Every refusal —
no code for the number, spent, withdrawn, expired, exhausted, wrong — is the
same answer, because telling them apart would tell a guesser which half of a
guess was right. The span records which it was.

The count is written in the caller's transaction, so a refusal is a result
rather than an error: Redeem answers false with a nil error, and a caller that
writes the natural `return err` out of its WithTransaction callback commits the
count. ErrCodeInvalid is the sentinel a caller reports the refusal with, once
the transaction has committed.

# One live code per number

The table's key is (scope, phone number), and an issue is an upsert onto it.
Issuing again replaces the prior code outright, so a person who asks twice and
enters the second code succeeds and a person who enters the first fails, and two
issues racing for one number converge on the later one rather than leaving two
live codes.

That is also the only issue-rate brake here. Issuance.Previous is the code an
issue replaced, and a consumer that wants a cooldown reads its IssuedAt and
declines to text. How often a caller may ask at all is primitives-go's
ratelimiting, in front of the consumer's door.

# What the digest is for, and what it is not

A code is stored as the digest of the row's id and the code, never as the code.
That keeps the plaintext out of a backup, a replica and a support engineer's
SELECT. It does not keep six digits from somebody holding a dump — a million
candidates per row is no work at all — and nothing could. What bounds a dump
reader is the lifetime: a code lives minutes, and a dump is read in days. The
digest is compared in the spend's own predicate, and no read projects it.

# What this store does not do

It sends nothing. Issue returns the plaintext, and the consumer texts it through
primitives-go's sms after the transaction commits, the way passwordreset mails
after its commit rather than inside it: a code texted for a transaction that
then rolled back is a code that will never redeem.

It reads no directory, so it does not know whether the subject exists or
whether the number is theirs. Whoever typed the number in decided that.

It opens no transaction. Every write takes the caller's database.Tx and every
read the wider database.SQLQueryExecutor. [SQLStore.Sweep] is the exception and
the usual one: a worker on a timer is the component servicing itself, so it runs
on the handle the store was built with.

# SQLite and whole seconds

SQLite stores these instants as whole-second text, so a deadline is truncated
down there: a code on that engine dies up to a second early rather than living a
second late, which is the direction that fails closed.
*/
package phonecodes
