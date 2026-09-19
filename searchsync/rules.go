package searchsync

import (
	"context"

	"github.com/primandproper/platform-go/v14/outbox"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// Change is what an enqueued payload has to be able to answer for index events
// to be derived from it. A message whose payload is not a Change is passed
// over, which is what lets one side effect sit on a Writer carrying every kind
// of message a service enqueues.
//
// Both methods are prefixed rather than spelled EventType and DocumentID. The
// payloads that implement them are data-change messages, and a data-change
// message already carries a field by the first of those names — a Go type
// cannot have a field and a method spelled the same way. A payload that had to
// be renamed to become indexable would be a payload whose wire format this
// package had decided.
type Change interface {
	// IndexEventType names what happened to the row. It is what a Rule is
	// matched on, and it is the application's vocabulary throughout: this
	// package never interprets it, only compares it.
	IndexEventType() string

	// IndexDocumentID returns the identifier stored under key, reporting false
	// when the payload carries none.
	//
	// It takes a key rather than returning "the payload's ID" because one
	// data-change message routinely names several entities — the recipe, the
	// step that changed, the meal plan they belong to — and which of them a
	// given index is keyed by is the rule's business rather than the payload's.
	IndexDocumentID(key string) (string, bool)
}

// Rule is one row of the mapping from what changed to what an index owes: the
// data-change event type it matches, the topic the derived index event is
// published to, where in the payload that index's document ID is found, and
// what the index should do about it.
//
// A row rather than a case in a switch, and that is the whole of it. Adding an
// entity to the search pipeline is a line in a table the application already
// has in front of it — a table that can be read end to end, counted against the
// list of indexes the service actually runs, and diffed in review. A switch can
// only be extended, and the reviewer who would have noticed the missing entity
// is reading a diff that added no line where the missing one would have gone.
//
// Several rules may name one event type, which is how a change that shows up in
// two indexes writes two events. Nothing derives a rule from another rule: the
// events a side effect returns are never themselves fed back through the table,
// because outbox hands a side effect only what the caller enqueued.
type Rule struct {
	// EventType is the data-change event type this rule matches, compared
	// against Change.IndexEventType.
	EventType string

	// Topic is where the derived index event is published, and is the topic the
	// jobs.Pool feeding that index's Syncer consumes. It is spelled here and in
	// the IndexSpec that registers the consuming side, which are routinely two
	// processes — an application declares it once as a constant and uses it in
	// both.
	Topic string

	// IDKey names the identifier the derived event carries, looked up through
	// Change.IndexDocumentID. It must be the ID the index keys documents by and
	// the ID the Fetcher on the other end reads back, since those are the same
	// document.
	IDKey string

	// Op is what the index owes: OpUpsert for a row that was written, OpDelete
	// for one that is gone. An upsert whose row has since vanished is applied
	// as a delete by the Syncer, so a create and an update are one rule rather
	// than two, and only a genuine removal needs its own.
	Op Op
}

// NewSideEffect builds the outbox side effect that derives index events from
// the data-change messages a transaction already enqueues.
//
// Register it on the Writer and the index event stops being something each
// repository method has to remember:
//
//	effect, err := searchsync.NewSideEffect(indexRules)
//	if err != nil {
//	    return err
//	}
//
//	writer, err := outbox.NewWriter(client.Dialect(),
//	    outbox.WithWriterSideEffect("search-index", effect))
//
// This is the registered form the package documentation contrasts with writing
// the event at the call site. Nothing about a repository method that writes a
// recipe says the write owes an index event, so the next one enqueues its
// data-change message alone, compiles, and passes review — and the index is
// wrong from then until the next rebuild, with nothing in between able to
// notice, because the event that went missing is one no consumer was waiting
// for. Derived here, every transaction that enqueues a data change writes the
// index events that change implies, by the same statement, whether or not
// whoever wrote it was thinking about search.
//
// The rules are validated once, here, rather than on every enqueue: a rule
// with no event type, no topic, no ID key or an op this package does not know
// is a wiring mistake, and the place to find it is the wiring rather than the
// first write of that entity in production. Two rules that agree on all three
// of event type, topic and ID key are refused for the same reason — they derive
// the same event twice from one change, which is a line that was pasted rather
// than written.
//
// A payload that is not a Change derives nothing, and an event type no rule
// names derives nothing. Both are ordinary: an outbox carries every message the
// service enqueues, and only some of them are about indexed entities. A rule
// that matches and finds no ID under its key is the one case that fails, and it
// fails the caller's transaction — see ErrMissingDocumentID.
func NewSideEffect(rules []Rule) (outbox.SideEffect, error) {
	if len(rules) == 0 {
		return nil, ErrNoRules
	}

	byEventType := make(map[string][]Rule, len(rules))

	for i := range rules {
		rule := &rules[i]

		switch {
		case rule.EventType == "":
			return nil, platformerrors.Wrapf(ErrInvalidRule, "rule %d has no event type", i)
		case rule.Topic == "":
			return nil, platformerrors.Wrapf(ErrInvalidRule, "rule %d for event type %q has no topic", i, rule.EventType)
		case rule.IDKey == "":
			return nil, platformerrors.Wrapf(ErrInvalidRule, "rule %d for event type %q has no ID key", i, rule.EventType)
		case !rule.Op.Valid():
			return nil, platformerrors.Wrapf(ErrInvalidRule, "rule %d for event type %q has unknown op %q", i, rule.EventType, rule.Op)
		}

		taken := byEventType[rule.EventType]
		for j := range taken {
			if taken[j].Topic == rule.Topic && taken[j].IDKey == rule.IDKey {
				return nil, platformerrors.Wrapf(ErrDuplicateRule,
					"rule %d repeats event type %q into topic %q on ID key %q", i, rule.EventType, rule.Topic, rule.IDKey)
			}
		}

		byEventType[rule.EventType] = append(taken, *rule)
	}

	return func(_ context.Context, _ database.Tx, msgs []outbox.Message) ([]outbox.Message, error) {
		var events []outbox.Message

		for i := range msgs {
			change, ok := msgs[i].Payload.(Change)
			if !ok {
				continue
			}

			matched := byEventType[change.IndexEventType()]
			for j := range matched {
				rule := &matched[j]

				documentID, found := change.IndexDocumentID(rule.IDKey)
				if !found || documentID == "" {
					return nil, platformerrors.Wrapf(ErrMissingDocumentID,
						"event type %q carries no %q for topic %q", rule.EventType, rule.IDKey, rule.Topic)
				}

				events = append(events, NewEvent(rule.Op, documentID).Message(rule.Topic))
			}
		}

		return events, nil
	}, nil
}
