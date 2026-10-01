package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. The mechanism is issuereports/grpc's
// and webhooks/grpc's: a method added to mediaregistry.Store later is in neither
// list until somebody says which.

// servedAs is every store method this service serves under a name other than
// its own, with the RPC that serves it.
var servedAs = map[string]string{
	// The caller's own objects: the owner is who is asking, so the RPC is named
	// for the caller rather than for an owner a request could name.
	"ListObjectsByOwner": "ListMyObjects",
}

// absent is every store method this service deliberately does not serve, with
// the reason recorded beside it.
var absent = map[string]string{
	// A key is not an address. mediaregistry/http's documentation makes the
	// argument, and serving a read by key here would teach every client the
	// habit the guarded serve exists to break.
	"GetObjectByKey": "a key is not an address",

	// Every object in the tenant, whoever owns it, is an operator's read: a
	// deployment serves it from its own console, under its own grant.
	"ListObjects": "a tenant-wide listing is an operator's read",

	// Erasure machinery, reached through mediaregistry/privacy from the
	// deployment's erasure run so it commits with the rest of a subject's
	// footprint. An RPC is a caller choosing when that commit happens.
	"ArchiveObjectsForOwner": "erasure machinery, committing with the rest of a subject's footprint",
}

func storeMethods() []string {
	t := reflect.TypeFor[mediaregistry.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

func rpcNames() []string {
	desc := mediaregistrypb.MediaRegistryService_ServiceDesc

	out := make([]string, 0, len(desc.Methods)+len(desc.Streams))
	for _, m := range desc.Methods {
		out = append(out, m.MethodName)
	}

	for i := range desc.Streams {
		out = append(out, desc.Streams[i].StreamName)
	}

	return out
}

// TestEveryStoreMethodIsEitherServedOrRuledOut is the roster's forward
// direction: a store method in neither list is a decision nobody made.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off mediaregistry.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			rpc := method
			if renamed, ok := servedAs[method]; ok {
				rpc = renamed
			}

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, rpc)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"mediaregistry.Store.%s must be exactly one of served (as %s) and ruled out; served=%t ruled out=%t",
				method, rpc, onTheWire, ruledOut))
		})
	}
}

// TestNoRulingOutlivesItsMethod is the other direction: a row naming a store
// method that no longer exists is a roster describing a tree that has moved.
func TestNoRulingOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for method := range absent {
		test.SliceContains(T, methods, method)
	}

	for method := range servedAs {
		test.SliceContains(T, methods, method)
	}
}

// TestEveryRPCIsAStoreMethodOrTheUpload is the roster read from the wire's
// side: an RPC here serves a store method, or is the upload — which serves
// mediaregistry.StoreAndRecord's two halves rather than any one method.
func TestEveryRPCIsAStoreMethodOrTheUpload(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for _, rpc := range rpcNames() {
		if rpc == "UploadObject" {
			continue
		}

		served := slices.Contains(methods, rpc)
		for method, renamed := range servedAs {
			served = served || (renamed == rpc && slices.Contains(methods, method))
		}

		test.True(T, served, test.Sprintf("the %s RPC serves no mediaregistry.Store method", rpc))
	}
}
