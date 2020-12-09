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

package dynamicinformer

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamiclister"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// DynamicSharedInformerOption defines the functional option type for SharedInformerFactory.
type DynamicSharedInformerOption func(*dynamicSharedInformerFactory) *dynamicSharedInformerFactory

type dynamicSharedInformerFactory struct {
	client        dynamic.Interface
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

var _ DynamicSharedInformerFactory = &dynamicSharedInformerFactory{}

// WithOnListErr sets the onListErrFunc that is added to the stop options
// when calling StartWithStopOptions.
// This method results in every informer in this factory getting the same stop options.
func WithOnListError(onListError cache.OnListErrorFunc) DynamicSharedInformerOption {
	return func(factory *dynamicSharedInformerFactory) *dynamicSharedInformerFactory {
		factory.onListError = onListError
		return factory
	}
}

// WithTweakListOptions sets a custom filter on all listers of the configured SharedInformerFactory.
func WithTweakListOptions(tweakListOptions TweakListOptionsFunc) DynamicSharedInformerOption {
	return func(factory *dynamicSharedInformerFactory) *dynamicSharedInformerFactory {
		factory.tweakListOptions = tweakListOptions
		return factory
	}
}

// WithNamespace limits the SharedInformerFactory to the specified namespace.
func WithNamespace(namespace string) DynamicSharedInformerOption {
	return func(factory *dynamicSharedInformerFactory) *dynamicSharedInformerFactory {
		factory.namespace = namespace
		return factory
	}
}

// NewDynamicSharedInformerFactory constructs a new instance of dynamicSharedInformerFactory for all namespaces.
func NewDynamicSharedInformerFactory(client dynamic.Interface, defaultResync time.Duration) DynamicSharedInformerFactory {
	return NewDynamicSharedInformerFactoryWithOptions(client, defaultResync)
}

// NewFilteredDynamicSharedInformerFactory constructs a new instance of dynamicSharedInformerFactory.
// Listers obtained via this factory will be subject to the same filters as specified here.
// Deprecated: Please use NewDynamicSharedInformerFactoryWithOptions instead
func NewFilteredDynamicSharedInformerFactory(client dynamic.Interface, defaultResync time.Duration, namespace string, tweakListOptions TweakListOptionsFunc) DynamicSharedInformerFactory {
	return NewDynamicSharedInformerFactoryWithOptions(client, defaultResync, WithNamespace(namespace), WithTweakListOptions(tweakListOptions))
}

// NewDynamicSharedInformerFactoryWithOptions constructs a new instance of a dynamicSharedInformerFactory with additional options.
func NewDynamicSharedInformerFactoryWithOptions(client dynamic.Interface, defaultResync time.Duration, options ...DynamicSharedInformerOption) DynamicSharedInformerFactory {
	factory := &dynamicSharedInformerFactory{
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

func (f *dynamicSharedInformerFactory) DoneChannelFor(gvr schema.GroupVersionResource) (cache.DoneChannel, bool) {
	f.lock.Lock()
	defer f.lock.Unlock()

	doneCh, ok := f.stoppableInformers[gvr]
	return doneCh, ok
}

func (f *dynamicSharedInformerFactory) ForResource(gvr schema.GroupVersionResource) informers.GenericInformer {
	f.lock.Lock()
	defer f.lock.Unlock()

	key := gvr
	informer, exists := f.informers[key]
	if exists {
		return informer
	}

	informer = NewFilteredDynamicInformer(f.client, gvr, f.namespace, f.defaultResync, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
	f.informers[key] = informer

	return informer
}

// Start initializes all requested informers.
func (f *dynamicSharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			go informer.Informer().Run(stopCh)
			f.startedInformers[informerType] = true
		}
	}
}

func (f *dynamicSharedInformerFactory) informerStopped(informerType schema.GroupVersionResource) {
	f.lock.Lock()
	defer f.lock.Unlock()
	delete(f.startedInformers, informerType)
	delete(f.informers, informerType)
}

// StartWithStopOptions initializes all requested informers with their stop options.
func (f *dynamicSharedInformerFactory) StartWithStopOptions(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	onListError := func(error) bool {
		return false
	}
	if f.onListError != nil {
		onListError = f.onListError
	}
	stopOptions := cache.StopOptions{
		ExternalStop: stopCh,
		OnListError:  onListError,
	}
	for informerType, informer := range f.informers {
		informerType := informerType
		informer := informer
		if !f.startedInformers[informerType] {
			go func() {
				defer f.informerStopped(informerType)
				informer.Informer().RunWithStopOptions(stopOptions)
				<-informer.Informer().StopHandle().Done()
			}()
			f.startedInformers[informerType] = true
			f.stoppableInformers[informerType] = informer.Informer().StopHandle().Done()
		}
	}

}

// WaitForCacheSync waits for all started informers' cache were synced.
func (f *dynamicSharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
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

// NewFilteredDynamicInformer constructs a new informer for a dynamic type.
func NewFilteredDynamicInformer(client dynamic.Interface, gvr schema.GroupVersionResource, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions TweakListOptionsFunc) informers.GenericInformer {
	return &dynamicInformer{
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
			&unstructured.Unstructured{},
			resyncPeriod,
			indexers,
		),
	}
}

type dynamicInformer struct {
	informer cache.SharedIndexInformer
	gvr      schema.GroupVersionResource
}

var _ informers.GenericInformer = &dynamicInformer{}

func (d *dynamicInformer) Informer() cache.SharedIndexInformer {
	return d.informer
}

func (d *dynamicInformer) Lister() cache.GenericLister {
	return dynamiclister.NewRuntimeObjectShim(dynamiclister.New(d.informer.GetIndexer(), d.gvr))
}
