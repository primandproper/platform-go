package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ClientToProto renders a registration for the wire.
//
// It carries no secret and no scope, and neither is an omission. The plaintext
// secret exists on one message in this schema and this is not it — see
// [IssuedClientToProto]. The registry is the caller's own, read off their
// principal, so a response naming it would be telling a client something it
// supplied.
//
// It is exported because a consumer composing this registry into a larger
// response — an admin console assembling a page — otherwise writes the same
// twelve assignments and gets one of them wrong.
func ClientToProto(c *oauth2clients.Client) *oauth2clientspb.OAuth2Client {
	if c == nil {
		return nil
	}

	out := &oauth2clientspb.OAuth2Client{
		CreatedAt:     timestamppb.New(c.CreatedAt),
		Id:            c.ID,
		ClientId:      c.ClientID,
		Name:          c.Name,
		Description:   c.Description,
		RedirectUris:  c.RedirectURIs,
		Scopes:        c.Scopes,
		BelongsToUser: c.BelongsToUser,
	}

	// The two nullable times stay unset rather than becoming the zero
	// timestamp: a client rendering "last updated" wants to know there was no
	// update, and 1970 is not that answer.
	if c.LastUpdatedAt != nil {
		out.LastUpdatedAt = timestamppb.New(*c.LastUpdatedAt)
	}

	if c.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*c.ArchivedAt)
	}

	return out
}

// IssuedClientToProto renders a freshly minted registration and its secret.
//
// It is the only converter in this package that puts a credential on the wire,
// and it is reachable only from the two creation RPCs, which is the whole reason
// oauth2clients.IssuedClient is a separate type from oauth2clients.Client.
func IssuedClientToProto(i *oauth2clients.IssuedClient) *oauth2clientspb.IssuedOAuth2Client {
	if i == nil {
		return nil
	}

	return &oauth2clientspb.IssuedOAuth2Client{
		Client:       ClientToProto(i.Client),
		ClientSecret: i.Secret,
	}
}

// ClientsToProto renders a page of registrations.
func ClientsToProto(clients []*oauth2clients.Client) []*oauth2clientspb.OAuth2Client {
	out := make([]*oauth2clientspb.OAuth2Client, 0, len(clients))
	for _, c := range clients {
		out = append(out, ClientToProto(c))
	}

	return out
}

// creationInputFromProto reads a registration request.
//
// A nil message is nil rather than an empty input, so a request that named no
// input is refused as malformed instead of being registered as a client with no
// name — which the store would refuse anyway, with a message about the name
// rather than about the request.
func creationInputFromProto(in *oauth2clientspb.OAuth2ClientCreationInput) *oauth2clients.CreationInput {
	if in == nil {
		return nil
	}

	return &oauth2clients.CreationInput{
		Name:         in.GetName(),
		Description:  in.GetDescription(),
		RedirectURIs: in.GetRedirectUris(),
		Scopes:       in.GetScopes(),
	}
}
