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
	"k8s.io/klog/v2"
)

// NewSharedInformerFactory constructs a new instance of metadataSharedInformerFactory for all namespaces.
func NewSharedInformerFactory(client metadata.Interface, defaultResync time.Duration) SharedInformerFactory {
	return NewStoppableSharedInformerFactory(client, defaultResync, metav1.NamespaceAll, nil, nil)
}

// NewFilteredSharedInformerFactory constructs a new instance of metadataSharedInformerFactory.
// Listers obtained via this factory will be subject to the same filters as specified here.
func NewFilteredSharedInformerFactory(client metadata.Interface, defaultResync time.Duration, namespace string, tweakListOptions TweakListOptionsFunc) SharedInformerFactory {
	return NewStoppableSharedInformerFactory(client, defaultResync, namespace, tweakListOptions, nil)
}

// NewStoppableSharedInformer constructs a new instance of metadataSahredInformerFactory
// that is ran with customizable stop options.
// TODO: Consider using a factory instead of ballooning constructors.
func NewStoppableSharedInformerFactory(client metadata.Interface, defaultResync time.Duration, namespace string, tweakListOptions TweakListOptionsFunc, onListError cache.OnListErrorFunc) SharedInformerFactory {
	return &metadataSharedInformerFactory{
		client:           client,
		defaultResync:    defaultResync,
		namespace:        namespace,
		informers:        map[schema.GroupVersionResource]informers.GenericInformer{},
		startedInformers: make(map[schema.GroupVersionResource]bool),
		tweakListOptions: tweakListOptions,
		onListError:      onListError,
	}
}

type metadataSharedInformerFactory struct {
	client        metadata.Interface
	defaultResync time.Duration
	namespace     string

	lock      sync.Mutex
	informers map[schema.GroupVersionResource]informers.GenericInformer
	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	startedInformers map[schema.GroupVersionResource]bool
	tweakListOptions TweakListOptionsFunc
	onListError      cache.OnListErrorFunc
}

var _ SharedInformerFactory = &metadataSharedInformerFactory{}

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
		informerType := informerType
		informer := informer
		if !f.startedInformers[informerType] {
			//go informer.Informer().Run(stopCh)
			go func() {
				informer.Informer().Run(stopCh)
			}()

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

// StartWithStopOptions initializes all requested informers with their stop options.
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
		ExternalStop: stopCh,
		OnListError:  onListError,
	}
	for informerType, informer := range f.informers {
		informerType := informerType
		informer := informer
		if !f.startedInformers[informerType] {
			go func() {
				defer f.informerStopped(informerType)
				klog.V(4).Infof("metainformer starting WSO")
				informer.Informer().RunWithStopOptions(stopOptions)
				<-informer.Informer().Done().Done()
			}()
			f.startedInformers[informerType] = true
		}
	}

}

// WaitForCacheSync waits for all started informers' cache were synced.
func (f *metadataSharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
	klog.V(4).Infof("meta WFCS begin")
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
	klog.V(4).Infof("meta WFCS end")
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
