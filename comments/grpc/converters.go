package grpc

import (
	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// CommentToProto renders a comment for the wire.
//
// It carries no scope, and that is not an omission: [commentspb.Comment] has
// nowhere to put one, because every row a response returns belongs to the scope
// the connection resolved — so a scope field would be telling a client
// something it supplied, and reserving the name is what keeps somebody from
// adding the assignment later.
//
// The author is rendered as stored. It is a string in this package because
// comments does not own the directory people live in, so what a client gets
// back is whatever identifier the consumer's principal reported when the
// comment was written.
//
// It is exported because a consumer composing comments into a larger response —
// a page that renders a recipe and its discussion in one call — otherwise
// writes the same eight assignments and gets one of them wrong.
func CommentToProto(c *comments.Comment) *commentspb.Comment {
	if c == nil {
		return nil
	}

	out := &commentspb.Comment{
		CreatedAt: timestamppb.New(c.CreatedAt),
		Id:        c.ID,
		ParentId:  c.ParentID,
		Author:    c.Author,
		Body:      c.Body,
		Target:    TargetToProto(c.Target),
	}

	// The two nullable times stay unset rather than becoming the zero
	// timestamp: a client renders an "edited" marker from last_updated_at, and
	// 1970 is not the answer to "has anybody revised this".
	if c.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*c.LastUpdatedAt)
	}

	if c.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*c.ArchivedAt)
	}

	return out
}

// CommentsToProto renders a page of comments.
//
// It takes the pointer slice filtering.QueryFilteredResult hands back, so a
// handler passes page.Data straight in.
func CommentsToProto(in []*comments.Comment) []*commentspb.Comment {
	out := make([]*commentspb.Comment, 0, len(in))
	for _, c := range in {
		out = append(out, CommentToProto(c))
	}

	return out
}

// TargetToProto renders what a comment is about.
//
// The target type goes out as the string it is. See comments.proto for why it
// will not become a generated enum: the catalog is the consumer's, and an enum
// would put their vocabulary on this module's release cadence.
//
// The zero target renders as an empty message rather than as nil, so that a
// client reading target.type off a response never has to distinguish "no target
// message" from "a target naming nothing" — neither is a state a stored comment
// can be in, because the write refuses both.
func TargetToProto(t comments.Target) *commentspb.CommentTarget {
	return &commentspb.CommentTarget{Type: t.Type.String(), Id: t.ID}
}

// TargetFromProto reads a target off a request.
//
// A nil message is the zero target rather than an error, because the zero
// target is a value this package's write already has a reading for: a reply
// that names none adopts its parent's. A root that names none is
// comments.ErrEmptyTargetType, raised by the store against the same rule every
// other caller meets.
//
// It is exported for symmetry with [TargetToProto], and because a consumer
// wrapping this service — a handler that resolves a slug into a target before
// forwarding — otherwise reaches into the message twice.
func TargetFromProto(in *commentspb.CommentTarget) comments.Target {
	if in == nil {
		return comments.Target{}
	}

	return comments.Target{Type: comments.TargetType(in.GetType()), ID: in.GetId()}
}

// commentFromProto reads a creation request into the comment the store writes.
//
// A nil message is nil rather than an empty comment, so a request that named no
// comment is refused as malformed instead of being written as one with no body
// — which validation would refuse anyway, with a message about the body rather
// than about the request.
//
// Four fields on comments.Comment are deliberately not read from the request.
// Author comes off the principal, because authorship a caller could name is
// authorship that says whatever the caller wanted it to. Scope is not set at
// all — not from a field, and not from the principal either: the store binds
// the scope it is handed as an argument and refuses a comment that names a
// different one, so filling it in here would be this converter answering a
// question the write is about to answer for itself. ID is the store's to
// assign, and the timestamps are the database's.
func commentFromProto(in *commentspb.CommentInput, author string) *comments.Comment {
	if in == nil {
		return nil
	}

	return &comments.Comment{
		ParentID: in.GetParentId(),
		Author:   author,
		Body:     in.GetBody(),
		Target:   TargetFromProto(in.GetTarget()),
	}
}
