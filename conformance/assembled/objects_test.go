package assembled_test

import (
	"bytes"
	"context"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/mediaregistry"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
)

// register is the Registered action: what a consumer's upload handler does once
// a person has sent it bytes, which is to store them and record the row inside
// a transaction on the deployment's own client. No surface creates an object,
// so StoreAndRecord over the manager and store the composition root built is
// the end of the path every deployment takes.
func register(
	db database.Client,
	manager uploads.UploadManager,
	store mediaregistry.Store,
) func(context.Context, tenancy.Scope, string) (*conformance.RegisteredObject, error) {
	return func(ctx context.Context, scope tenancy.Scope, userID string) (*conformance.RegisteredObject, error) {
		// Distinct per object, so a route that served the wrong object's bytes
		// is told apart from one that served the right ones.
		content := []byte("conformance object " + identifiers.New())

		var recorded *mediaregistry.Object

		err := db.WithTransaction(ctx, func(tx database.Tx) error {
			object, err := mediaregistry.StoreAndRecord(ctx, tx, scope, manager, store, mediaregistry.ObjectInput{
				Key:         "conformance/" + identifiers.New() + ".txt",
				ContentType: "text/plain",
				OwnerID:     userID,
			}, bytes.NewReader(content))
			if err != nil {
				return err
			}

			recorded = object

			return nil
		})
		if err != nil {
			return nil, err
		}

		return &conformance.RegisteredObject{ID: recorded.ID, Content: content}, nil
	}
}
