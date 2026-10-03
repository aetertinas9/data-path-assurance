package ingestadapter

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
)

// nodeDirectory is the liveingest.NodeDirectory of the API server.
type nodeDirectory struct{ a *Adapter }

// validNodeName reports whether name can be the name of a Node: a DNS-1123
// subdomain. A name outside it is answered as "not found" without a request,
// the answer the API server would give, so that an agent cannot tell a missing
// Node from an unusable name and the name never reaches a request path.
func validNodeName(name string) bool {
	return len(validation.IsDNS1123Subdomain(name)) == 0
}

// GetNode reads the Node with the given name directly from the API server. A
// missing Node (or a name that no Node can have) is liveingest.ErrNodeNotFound;
// every other failure is liveingest.ErrStoreUnavailable. It starts no request
// once ctx is done.
func (d nodeDirectory) GetNode(ctx context.Context, name string) (liveingest.NodeInfo, error) {
	if !validNodeName(name) {
		return liveingest.NodeInfo{}, liveingest.ErrNodeNotFound
	}
	if d.a == nil || d.a.hello == nil { // not built by New
		return liveingest.NodeInfo{}, unavailable(nil)
	}
	if err := ctx.Err(); err != nil {
		return liveingest.NodeInfo{}, unavailable(err)
	}
	var node corev1.Node
	err := d.a.hello.Get(ctx, client.ObjectKey{Name: name}, &node)
	switch {
	case err == nil:
		return liveingest.NodeInfo{Name: node.Name, UID: string(node.UID)}, nil
	case apierrors.IsNotFound(err):
		return liveingest.NodeInfo{}, liveingest.ErrNodeNotFound
	case ctx.Err() != nil:
		return liveingest.NodeInfo{}, unavailable(ctx.Err())
	default:
		return liveingest.NodeInfo{}, unavailable(err)
	}
}
