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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// SharedInformerFactory provides access to a shared informer and lister for dynamic client
type SharedInformerFactory interface {
	Start(stopCh <-chan struct{})
	StartWithStopOptions(ctx context.Context)
	ForResource(gvr schema.GroupVersionResource) informers.GenericInformer
	DoneChannelFor(gvr schema.GroupVersionResource) (cache.DoneChannel, bool)
	WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool
}

// TweakListOptionsFunc defines the signature of a helper function
// that wants to provide more listing options to API
type TweakListOptionsFunc func(*metav1.ListOptions)
