package controller

import (
	"context"
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// informers are the four watch caches of the controller.
type informers struct {
	fleets, devices, paths, nodes cache.SharedIndexInformer
}

// newInformers builds the watch caches on clients made from cfg, so every
// request goes through the caller's transport settings. Watches must not be
// cut by the per-request Timeout, so they use a copy with the Timeout cleared.
// trigger is called for every event that should start a pass.
func newInformers(cfg *rest.Config, scheme *runtime.Scheme, trigger func()) (*informers, error) {
	watchCfg := rest.CopyConfig(cfg)
	watchCfg.Timeout = 0

	crdCfg := rest.CopyConfig(watchCfg)
	crdCfg.GroupVersion = &v1alpha1.GroupVersion
	crdCfg.APIPath = "/apis"
	crdCfg.NegotiatedSerializer = serializer.NewCodecFactory(scheme).WithoutConversion()
	crdClient, err := rest.RESTClientFor(crdCfg)
	if err != nil {
		return nil, err
	}
	coreClient, err := corev1client.NewForConfig(watchCfg)
	if err != nil {
		return nil, err
	}

	all := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { trigger() },
		UpdateFunc: func(any, any) { trigger() },
		DeleteFunc: func(any) { trigger() },
	}
	// A Node changes constantly (heartbeats, images); only label, UID and
	// deletion changes can alter the result (GKA-140).
	nodeHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { trigger() },
		UpdateFunc: func(oldObj, newObj any) {
			o, ok1 := oldObj.(*corev1.Node)
			n, ok2 := newObj.(*corev1.Node)
			if !ok1 || !ok2 || o.UID != n.UID || !maps.Equal(o.Labels, n.Labels) || (o.DeletionTimestamp == nil) != (n.DeletionTimestamp == nil) {
				trigger()
			}
		},
		DeleteFunc: func(any) { trigger() },
	}

	mk := func(getter cache.Getter, resource string, obj runtime.Object, h cache.ResourceEventHandler) (cache.SharedIndexInformer, error) {
		lw := cache.NewListWatchFromClient(getter, resource, "", fields.Everything())
		inf := cache.NewSharedIndexInformer(lw, obj, 0, cache.Indexers{})
		if _, err := inf.AddEventHandler(h); err != nil {
			return nil, err
		}
		return inf, nil
	}
	inf := &informers{}
	if inf.fleets, err = mk(crdClient, "gpufleets", &v1alpha1.GPUFleet{}, all); err != nil {
		return nil, err
	}
	if inf.devices, err = mk(crdClient, "gpudevices", &v1alpha1.GPUDevice{}, all); err != nil {
		return nil, err
	}
	if inf.paths, err = mk(crdClient, "nodepathstates", &v1alpha1.NodePathState{}, all); err != nil {
		return nil, err
	}
	if inf.nodes, err = mk(coreClient.RESTClient(), "nodes", &corev1.Node{}, nodeHandler); err != nil {
		return nil, err
	}
	return inf, nil
}

// run starts every informer; the returned WaitGroup finishes when all of them
// have stopped after ctx ended.
func (i *informers) run(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, inf := range []cache.SharedIndexInformer{i.fleets, i.devices, i.paths, i.nodes} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inf.RunWithContext(ctx)
		}()
	}
	return &wg
}

// waitSynced blocks, without a time limit, until every cache has synced or ctx
// ends. It reports whether they synced.
func (i *informers) waitSynced(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), i.fleets.HasSynced, i.devices.HasSynced, i.paths.HasSynced, i.nodes.HasSynced)
}

// listStore returns the objects of an informer's store as *T. The objects are
// shared with the cache and must not be modified.
func listStore[T any](inf cache.SharedIndexInformer) []*T {
	items := inf.GetStore().List()
	out := make([]*T, 0, len(items))
	for _, it := range items {
		if t, ok := it.(*T); ok {
			out = append(out, t)
		}
	}
	return out
}
