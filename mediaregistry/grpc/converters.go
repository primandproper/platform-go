package grpc

import (
	"time"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ObjectToProto renders a registry row for the wire. A nil row is a nil
// message. The scope is not rendered: every row a caller is handed belongs to
// the tenant they asked in, so it would tell them something they supplied.
func ObjectToProto(object *mediaregistry.Object) *mediaregistrypb.Object {
	if object == nil {
		return nil
	}

	return &mediaregistrypb.Object{
		CreatedAt:     timestamppb.New(object.CreatedAt),
		LastUpdatedAt: optionalTime(object.LastUpdatedAt),
		ArchivedAt:    optionalTime(object.ArchivedAt),
		BelongsTo:     SubjectToProto(object.BelongsTo),
		Id:            object.ID,
		Key:           object.Key,
		ContentType:   object.ContentType,
		OwnerId:       object.OwnerID,
		Size:          object.Size,
	}
}

// ObjectsToProto renders a set of rows, skipping any nil among them.
func ObjectsToProto(objects []*mediaregistry.Object) []*mediaregistrypb.Object {
	out := make([]*mediaregistrypb.Object, 0, len(objects))

	for _, object := range objects {
		if object != nil {
			out = append(out, ObjectToProto(object))
		}
	}

	return out
}

// SubjectToProto renders a subject, or nil for the subject that names nothing —
// so an unattached object carries no belongs_to rather than an empty one.
func SubjectToProto(subject mediaregistry.Subject) *mediaregistrypb.Subject {
	if subject.Type == "" && subject.ID == "" {
		return nil
	}

	return &mediaregistrypb.Subject{Type: subject.Type, Id: subject.ID}
}

// SubjectFromProto reads a subject off the wire. An absent one is the zero
// Subject, which names nothing.
func SubjectFromProto(subject *mediaregistrypb.Subject) mediaregistry.Subject {
	return mediaregistry.Subject{Type: subject.GetType(), ID: subject.GetId()}
}

func optionalTime(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}

	return timestamppb.New(*t)
}
