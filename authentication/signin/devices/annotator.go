package devices

import (
	"context"

	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// NewAnnotator answers what was recorded about the devices behind a person's
// logins, in the shape signin/grpc's WithSignInAnnotator takes. Each login's
// attributes are [Device.Attributes]: the Attribute keys, naming only what is
// known.
//
// A login with nothing recorded, or with a row whose every value is empty, gets
// no attributes. An error from the store fails the listing, which is what
// signingrpc.SignInAnnotator asks of an annotator.
//
// The executor is what the reads run on — Client.Reader() in ordinary wiring,
// since a listing is a read and a replica that has not yet seen the newest
// renewal shows the one before it. Both arguments are required.
func NewAnnotator(store Store, reader database.SQLQueryExecutor) (signingrpc.SignInAnnotator, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	return func(
		ctx context.Context,
		scope tenancy.Scope,
		userID string,
		familyIDs []string,
	) (map[string]map[string]string, error) {
		devices, err := store.ListForFamilies(ctx, reader, scope, userID, familyIDs)
		if err != nil {
			return nil, platformerrors.Wrap(err, "reading the devices behind a person's sign-ins")
		}

		annotations := make(map[string]map[string]string, len(devices))
		for _, device := range devices {
			if attributes := device.Attributes(); len(attributes) > 0 {
				annotations[device.FamilyID] = attributes
			}
		}

		return annotations, nil
	}, nil
}
