/*
Copyright The Kubernetes Authors.

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

// Code generated by informer-gen. DO NOT EDIT.

package externalversions

import (
	"fmt"

	schema "k8s.io/apimachinery/pkg/runtime/schema"
	cache "k8s.io/client-go/tools/cache"
	v1 "k8s.io/code-generator/examples/MixedCase/apis/example/v1"
)

// GenericInformer is type of SharedIndexInformer which will locate and delegate to other
// sharedInformers based on type
type GenericInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() cache.GenericLister
}

type genericInformer struct {
	informer cache.SharedIndexInformer
	resource schema.GroupResource
}

// StoppableInformerInfo contains an informer and a done channel
// indicating when that informer has been stopped.
type StoppableInformerInfo struct {
	// Informer is the stoppable generic informer that has been
	// stopped once Done has fired.
	Informer GenericInformer
	// Done is the channel indicating when the informer has stopped
	Done cache.DoneChannel
}

// Informer returns the SharedIndexInformer.
func (f *genericInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

// Lister returns the GenericLister.
func (f *genericInformer) Lister() cache.GenericLister {
	return cache.NewGenericLister(f.Informer().GetIndexer(), f.resource)
}

// ForResource gives generic access to a shared informer of the matching type
// TODO extend this to unknown resources with a client pool
func (f *sharedInformerFactory) ForResource(resource schema.GroupVersionResource) (GenericInformer, error) {
	switch resource {
	// Group=example.crd.code-generator.k8s.io, Version=v1
	case v1.SchemeGroupVersion.WithResource("clustertesttypes"):
		return &genericInformer{resource: resource.GroupResource(), informer: f.Example().V1().ClusterTestTypes().Informer()}, nil
	case v1.SchemeGroupVersion.WithResource("testtypes"):
		return &genericInformer{resource: resource.GroupResource(), informer: f.Example().V1().TestTypes().Informer()}, nil

	}

	return nil, fmt.Errorf("no informer found for %v", resource)
}

// ForStoppableResource returns the informer info (informer and done channel)
// indicating when the resource's informer is stopped.
// This exists to satisfy the InformerFactory interface,  but because sharedInformerFactory
// is only used with builtin types it is not expected to ever be called
// (because StartWithStopOptions is never used as builtin resources are never uninstalled from the cluster).
// Dynamicinformer and metadatainformer facotries actually implement ForStoppableResource.
func (f *sharedInformerFactory) ForStoppableResource(gvr schema.GroupVersionResource) (*StoppableInformerInfo, bool) {
	return nil, false
}
