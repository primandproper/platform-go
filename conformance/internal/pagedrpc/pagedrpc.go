// Package pagedrpc finds every paged read this module serves over gRPC — every
// RPC whose request carries a filtering.v1.QueryFilter — and builds a request
// for each that is answerable apart from its filter.
//
// Two suites read it: the one asserting a malformed filter is refused, and the
// one asserting a page reports the filter it applied. Both need the same two
// things, and both would be wrong in the same way with a second copy.
//
// # Found, and then answered
//
// The reads are found from each service's descriptor, so one added later is
// covered without anybody coming back here. What cannot be found that way is how
// to make the rest of the request answerable: a read listing an account's
// subscriptions needs an account, and a read of a comment thread needs a target
// the deployment declared. Without those the request fails on something other
// than its filter, and an assertion that a malformed filter is refused passes
// for the wrong reason.
//
// So the requests that need more than a filter are written out, one per read,
// in builders — enumerated rather than inferred from field names, because a
// guess at what "subject" means on a given read is exactly the thing that can
// be wrong quietly. TestEveryPagedRPCHasARequest keeps the table honest from the
// other side: a paged read whose request has a field beyond its filter and no
// entry here fails there, rather than being asserted against with an empty one.
package pagedrpc

import (
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/conformance/internal/services"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	// filterField is what every paged read names its filter. It is checked
	// rather than assumed: a read naming it otherwise has no filter this
	// package can set, and TestEveryPagedRPCHasARequest fails on it.
	filterField protoreflect.Name = "filter"

	queryFilterName protoreflect.FullName = "primandproper.platform.filtering.v1.QueryFilter"
	paginationName  protoreflect.FullName = "primandproper.platform.filtering.v1.Pagination"

	// absentID names nothing. A read keyed on it answers with an absence or an
	// empty page, either of which is a request answerable apart from its filter.
	absentID = "conformance-absent"
)

// RPC is one paged read.
type RPC struct {
	// Method is the read's descriptor.
	Method protoreflect.MethodDescriptor

	// Surface is the service it is on, as the subtests name it.
	Surface string

	// FullName is the method as a client invokes it.
	FullName string
}

// Mounted is every paged read on the surfaces the subject mounted.
func Mounted(surfaces *conformance.Surfaces) []RPC {
	all := All()
	out := make([]RPC, 0, len(all))

	for i := range all {
		if all[i].mounted(surfaces) {
			out = append(out, all[i])
		}
	}

	return out
}

// All is every paged read this module serves.
func All() []RPC {
	var out []RPC

	all := services.All()
	for i := range all {
		svc := &all[i]

		descriptor := svc.Descriptor()
		if descriptor == nil {
			continue
		}

		methods := descriptor.Methods()
		for i := range methods.Len() {
			method := methods.Get(i)

			if !hasFilter(method.Input()) {
				continue
			}

			out = append(out, RPC{
				Surface:  svc.Name,
				FullName: "/" + string(descriptor.FullName()) + "/" + string(method.Name()),
				Method:   method,
			})
		}
	}

	return out
}

func (r RPC) mounted(surfaces *conformance.Surfaces) bool {
	all := services.All()
	for i := range all {
		if all[i].Name == r.Surface {
			return all[i].Mounted(*surfaces)
		}
	}

	return false
}

// Request builds the read's request for subject, carrying filter.
//
// A non-empty reason means the read cannot be made answerable for this subject
// — it needs an identifier the subject does not know, or a comment target type
// the deployment did not name — and the caller skips with it.
func (r RPC) Request(
	subject *conformance.Subject,
	seams *conformance.Seams,
	filter *filteringpb.QueryFilter,
) (request proto.Message, skip string) {
	var msg proto.Message

	if build, ok := builders()[r.FullName]; ok {
		built, reason := build(subject, seams)
		if reason != "" {
			return nil, reason
		}

		msg = built
	} else {
		msg = dynamicpb.NewMessage(r.Method.Input())
	}

	field := msg.ProtoReflect().Descriptor().Fields().ByName(filterField)
	if field == nil {
		return nil, "its request names its filter something other than " + string(filterField)
	}

	if filter != nil {
		msg.ProtoReflect().Set(field, protoreflect.ValueOfMessage(filter.ProtoReflect()))
	}

	return msg, ""
}

// Response is an empty message of the read's response type, to invoke into.
func (r RPC) Response() *dynamicpb.Message {
	return dynamicpb.NewMessage(r.Method.Output())
}

// ErrNoPagination is a paged read's response that carries no
// filtering.v1.Pagination: a page that says nothing about how it was cut.
var ErrNoPagination = platformerrors.New("a paged read answered with no pagination")

// Pagination reads the page's filtering.v1.Pagination out of a response, and
// reports ErrNoPagination where the response carries none.
//
// It is read by reflection and converted through the wire format, because the
// response is a dynamic message and the suites want the generated type.
func Pagination(response proto.Message) (*filteringpb.Pagination, error) {
	found := findMessage(response.ProtoReflect(), paginationName, 0)
	if found == nil {
		return nil, ErrNoPagination
	}

	raw, err := proto.Marshal(found.Interface())
	if err != nil {
		return nil, err
	}

	out := &filteringpb.Pagination{}
	if err = proto.Unmarshal(raw, out); err != nil {
		return nil, err
	}

	return out, nil
}

func findMessage(m protoreflect.Message, name protoreflect.FullName, depth int) protoreflect.Message {
	if depth > 2 {
		return nil
	}

	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind || field.IsList() || field.IsMap() || !m.Has(field) {
			continue
		}

		value := m.Get(field).Message()
		if field.Message().FullName() == name {
			return value
		}

		if found := findMessage(value, name, depth+1); found != nil {
			return found
		}
	}

	return nil
}

func hasFilter(input protoreflect.MessageDescriptor) bool {
	fields := input.Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		if field.Kind() == protoreflect.MessageKind && field.Message().FullName() == queryFilterName {
			return true
		}
	}

	return false
}

// builder makes one read's request answerable apart from its filter.
type builder func(subject *conformance.Subject, seams *conformance.Seams) (proto.Message, string)

// subjectUser is the subject type named where a read takes one; see builders.
const subjectUser = "user"

const (
	needsUser    = "it names the caller, and this subject does not surface its user identifier"
	needsAccount = "it names the caller's account, and this subject does not surface its account identifier"
	needsTarget  = "it names a comment target, and this subject declares no comment target type (Seams.CommentTargetType)"
)

// builders are the paged reads whose requests carry more than a filter, each
// made answerable with the caller's own identifiers where the read names a
// person or an account, and with an identifier nothing holds where it names a
// row — an absence is an answer, and what is being asserted is the filter.
//
// "user" is the subject type named where a read takes one, because it is the
// one every surface here suggests. A deployment whose vocabulary differs
// refuses it on its own terms, which the suites' positive controls report.
func builders() map[string]builder {
	return map[string]builder{
		// Every field of an EntryQuery is an optional narrowing, so the empty
		// one is the whole log — answerable, and written out so that it reads
		// as a decision rather than an omission.
		auditpb.AuditService_ListEntries_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &auditpb.ListEntriesRequest{Query: &auditpb.EntryQuery{}}, ""
		},

		billingpb.BillingService_ListSubscriptionsForAccount_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.AccountID == "" {
				return nil, needsAccount
			}

			return &billingpb.ListSubscriptionsForAccountRequest{AccountId: s.AccountID}, ""
		},
		billingpb.BillingService_ListCurrentSubscriptions_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.AccountID == "" {
				return nil, needsAccount
			}

			return &billingpb.ListCurrentSubscriptionsRequest{AccountId: s.AccountID}, ""
		},
		billingpb.BillingService_ListPurchasesForAccount_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.AccountID == "" {
				return nil, needsAccount
			}

			return &billingpb.ListPurchasesForAccountRequest{AccountId: s.AccountID}, ""
		},
		billingpb.BillingService_ListTransactionsForAccount_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.AccountID == "" {
				return nil, needsAccount
			}

			return &billingpb.ListTransactionsForAccountRequest{AccountId: s.AccountID}, ""
		},

		commentspb.CommentsService_ListRootComments_FullMethodName: func(_ *conformance.Subject, seams *conformance.Seams) (proto.Message, string) {
			if seams.CommentTargetType == "" {
				return nil, needsTarget
			}

			return &commentspb.ListRootCommentsRequest{
				Target: &commentspb.CommentTarget{Type: seams.CommentTargetType, Id: absentID},
			}, ""
		},
		commentspb.CommentsService_ListReplies_FullMethodName: func(_ *conformance.Subject, seams *conformance.Seams) (proto.Message, string) {
			if seams.CommentTargetType == "" {
				return nil, needsTarget
			}

			return &commentspb.ListRepliesRequest{
				Target:   &commentspb.CommentTarget{Type: seams.CommentTargetType, Id: absentID},
				ParentId: absentID,
			}, ""
		},
		commentspb.CommentsService_ListCommentsByTargetType_FullMethodName: func(_ *conformance.Subject, seams *conformance.Seams) (proto.Message, string) {
			if seams.CommentTargetType == "" {
				return nil, needsTarget
			}

			return &commentspb.ListCommentsByTargetTypeRequest{TargetType: seams.CommentTargetType}, ""
		},
		commentspb.CommentsService_ListCommentsByAuthor_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.UserID == "" {
				return nil, needsUser
			}

			return &commentspb.ListCommentsByAuthorRequest{Author: s.UserID}, ""
		},

		identitypb.IdentityService_SearchUsersByUsername_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &identitypb.SearchUsersByUsernameRequest{Prefix: "conf"}, ""
		},
		identitypb.IdentityService_ListAccountsForUser_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.UserID == "" {
				return nil, needsUser
			}

			return &identitypb.ListAccountsForUserRequest{UserId: s.UserID}, ""
		},
		identitypb.IdentityService_ListAccountMembers_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.AccountID == "" {
				return nil, needsAccount
			}

			return &identitypb.ListAccountMembersRequest{AccountId: s.AccountID}, ""
		},

		issuereportspb.IssueReportsService_ListReportsByStatus_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &issuereportspb.ListReportsByStatusRequest{Status: issuereportspb.ReportStatus_REPORT_STATUS_OPEN}, ""
		},
		issuereportspb.IssueReportsService_ListReportsByReporter_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.UserID == "" {
				return nil, needsUser
			}

			return &issuereportspb.ListReportsByReporterRequest{Reporter: s.UserID}, ""
		},
		issuereportspb.IssueReportsService_ListReportsBySubjectType_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &issuereportspb.ListReportsBySubjectTypeRequest{SubjectType: subjectUser}, ""
		},
		issuereportspb.IssueReportsService_ListReportsForSubject_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.UserID == "" {
				return nil, needsUser
			}

			return &issuereportspb.ListReportsForSubjectRequest{SubjectType: subjectUser, SubjectId: s.UserID}, ""
		},

		settingspb.SettingsService_ListValuesForDefinition_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &settingspb.ListValuesForDefinitionRequest{Name: absentID}, ""
		},
		settingspb.SettingsService_ListValuesForSubject_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.UserID == "" {
				return nil, needsUser
			}

			return &settingspb.ListValuesForSubjectRequest{
				Subject: &settingspb.SettingSubject{Type: subjectUser, Id: s.UserID},
			}, ""
		},

		waitlistspb.WaitlistsService_ListSignups_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &waitlistspb.ListSignupsRequest{ListId: absentID}, ""
		},
		waitlistspb.WaitlistsService_ListSignupsForSubject_FullMethodName: func(s *conformance.Subject, _ *conformance.Seams) (proto.Message, string) {
			if s.UserID == "" {
				return nil, needsUser
			}

			return &waitlistspb.ListSignupsForSubjectRequest{
				Subject: &waitlistspb.SignupSubject{Type: subjectUser, Id: s.UserID},
			}, ""
		},

		webhookspb.WebhooksService_ListSubscriptions_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &webhookspb.ListSubscriptionsRequest{EndpointId: absentID}, ""
		},
		webhookspb.WebhooksService_ListAttempts_FullMethodName: func(*conformance.Subject, *conformance.Seams) (proto.Message, string) {
			return &webhookspb.ListAttemptsRequest{DeliveryId: absentID}, ""
		},
	}
}
