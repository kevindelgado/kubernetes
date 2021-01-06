/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metadatainformer

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatalister"
	"k8s.io/client-go/tools/cache"
)

// SharedInformerOption defines the functional option type for SharedInformerFactory.
type SharedInformerOption func(*metadataSharedInformerFactory) *metadataSharedInformerFactory

type metadataSharedInformerFactory struct {
	client        metadata.Interface
	defaultResync time.Duration
	namespace     string

	lock      sync.Mutex
	informers map[schema.GroupVersionResource]informers.GenericInformer
	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	startedInformers   map[schema.GroupVersionResource]bool
	stoppableInformers map[schema.GroupVersionResource]cache.DoneChannel
	tweakListOptions   TweakListOptionsFunc
	onListError        cache.OnListErrorFunc
}

var _ SharedInformerFactory = &metadataSharedInformerFactory{}

// WithOnListError sets the onListErrFunc that is added to the stop options
// when calling StartWithStopOptions.
// This method results in every informer in this factory getting the same stop options.
func WithOnListError(onListError cache.OnListErrorFunc) SharedInformerOption {
	return func(factory *metadataSharedInformerFactory) *metadataSharedInformerFactory {
		factory.onListError = onListError
		return factory
	}
}

// WithTweakListOptions sets a custom filter on all listers of the configured SharedInformerFactory.
func WithTweakListOptions(tweakListOptions TweakListOptionsFunc) SharedInformerOption {
	return func(factory *metadataSharedInformerFactory) *metadataSharedInformerFactory {
		factory.tweakListOptions = tweakListOptions
		return factory
	}
}

// WithNamespace limits the SharedInformerFactory to the specified namespace.
func WithNamespace(namespace string) SharedInformerOption {
	return func(factory *metadataSharedInformerFactory) *metadataSharedInformerFactory {
		factory.namespace = namespace
		return factory
	}
}

// NewSharedInformerFactory constructs a new instance of metadataSharedInformerFactory for all namespaces.
func NewSharedInformerFactory(client metadata.Interface, defaultResync time.Duration) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync)
}

// NewFilteredSharedInformerFactory constructs a new instance of metadataSharedInformerFactory.
// Listers obtained via this factory will be subject to the same filters as specified here.
// Deprecated: Please use NewSharedInformerFactoryWithOptions instead
func NewFilteredSharedInformerFactory(client metadata.Interface, defaultResync time.Duration, namespace string, tweakListOptions TweakListOptionsFunc) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync, WithNamespace(namespace), WithTweakListOptions(tweakListOptions))
}

// NewSharedInformerFactoryWithOptions constructs a new instance of a SharedInformerFactory with additional options.
func NewSharedInformerFactoryWithOptions(client metadata.Interface, defaultResync time.Duration, options ...SharedInformerOption) SharedInformerFactory {
	factory := &metadataSharedInformerFactory{
		client:             client,
		defaultResync:      defaultResync,
		namespace:          metav1.NamespaceAll,
		informers:          map[schema.GroupVersionResource]informers.GenericInformer{},
		startedInformers:   make(map[schema.GroupVersionResource]bool),
		stoppableInformers: make(map[schema.GroupVersionResource]cache.DoneChannel),
	}

	// Apply all options
	for _, opt := range options {
		factory = opt(factory)
	}

	return factory
}

func (f *metadataSharedInformerFactory) DoneChannelFor(gvr schema.GroupVersionResource) (cache.DoneChannel, bool) {
	f.lock.Lock()
	defer f.lock.Unlock()

	doneCh, ok := f.stoppableInformers[gvr]
	return doneCh, ok
}

func (f *metadataSharedInformerFactory) ForResource(gvr schema.GroupVersionResource) informers.GenericInformer {
	f.lock.Lock()
	defer f.lock.Unlock()

	key := gvr
	informer, exists := f.informers[key]
	if exists {
		return informer
	}

	informer = NewFilteredMetadataInformer(f.client, gvr, f.namespace, f.defaultResync, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
	f.informers[key] = informer

	return informer
}

// Start initializes all requested informers.
func (f *metadataSharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			go informer.Informer().Run(stopCh)
			f.startedInformers[informerType] = true
		}
	}
}

func (f *metadataSharedInformerFactory) informerStopped(informerType schema.GroupVersionResource) {
	f.lock.Lock()
	defer f.lock.Unlock()
	delete(f.startedInformers, informerType)
	delete(f.informers, informerType)
}

// StartWithStopOptions initializes all requested informers with the stop options provided, defaulting to
// the old stop options (only stopping via closure of stopCh).
// It makes sure remove an informer from the list of informers and started informers when the informer is stopped
// to prevent a race where InformerFor gives the user back a stopped informer that will never be started again.
func (f *metadataSharedInformerFactory) StartWithStopOptions(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	onListError := func(error) bool {
		return false
	}
	if f.onListError != nil {
		onListError = f.onListError
	}
	stopOptions := cache.StopOptions{
		OnListError: onListError,
	}
	for informerType, informer := range f.informers {
		informerType := informerType
		informer := informer
		if !f.startedInformers[informerType] {
			infCtx, infCancel := context.WithCancel(context.TODO())
			go func(cancel context.CancelFunc) {
				defer cancel()
				<-stopCh
			}(infCancel)
			go func(ctx context.Context, cancel context.CancelFunc) {
				defer f.informerStopped(informerType)
				defer cancel()
				informer.Informer().RunWithStopOptions(ctx, stopOptions)
			}(infCtx, infCancel)
			f.startedInformers[informerType] = true
			f.stoppableInformers[informerType] = infCtx.Done()

		}
	}

}

// WaitForCacheSync waits for all started informers' cache were synced.
func (f *metadataSharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
	informers := func() map[schema.GroupVersionResource]cache.SharedIndexInformer {
		f.lock.Lock()
		defer f.lock.Unlock()

		informers := map[schema.GroupVersionResource]cache.SharedIndexInformer{}
		for informerType, informer := range f.informers {
			if f.startedInformers[informerType] {
				informers[informerType] = informer.Informer()
			}
		}
		return informers
	}()

	res := map[schema.GroupVersionResource]bool{}
	for informType, informer := range informers {
		res[informType] = cache.WaitForCacheSync(stopCh, informer.HasSynced)
	}
	return res
}

// NewFilteredMetadataInformer constructs a new informer for a metadata type.
func NewFilteredMetadataInformer(client metadata.Interface, gvr schema.GroupVersionResource, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions TweakListOptionsFunc) informers.GenericInformer {
	return &metadataInformer{
		gvr: gvr,
		informer: cache.NewSharedIndexInformer(
			&cache.ListWatch{
				ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
					if tweakListOptions != nil {
						tweakListOptions(&options)
					}
					return client.Resource(gvr).Namespace(namespace).List(context.TODO(), options)
				},
				WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
					if tweakListOptions != nil {
						tweakListOptions(&options)
					}
					return client.Resource(gvr).Namespace(namespace).Watch(context.TODO(), options)
				},
			},
			&metav1.PartialObjectMetadata{},
			resyncPeriod,
			indexers,
		),
	}
}

type metadataInformer struct {
	informer cache.SharedIndexInformer
	gvr      schema.GroupVersionResource
}

var _ informers.GenericInformer = &metadataInformer{}

func (d *metadataInformer) Informer() cache.SharedIndexInformer {
	return d.informer
}

func (d *metadataInformer) Lister() cache.GenericLister {
	return metadatalister.NewRuntimeObjectShim(metadatalister.New(d.informer.GetIndexer(), d.gvr))
}
