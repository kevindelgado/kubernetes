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
	"errors"
	"flag"
	"testing"
	"time"

	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/client-go/metadata/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

func init() {
	klog.InitFlags(flag.CommandLine)
	flag.CommandLine.Lookup("v").Value.Set("5")
	flag.CommandLine.Lookup("alsologtostderr").Value.Set("true")
}

func TestMetadataSharedInformerFactory(t *testing.T) {
	scenarios := []struct {
		name        string
		existingObj *metav1.PartialObjectMetadata
		gvr         schema.GroupVersionResource
		ns          string
		trigger     func(gvr schema.GroupVersionResource, ns string, fakeClient *fake.FakeMetadataClient, testObject *metav1.PartialObjectMetadata) *metav1.PartialObjectMetadata
		handler     func(rcvCh chan<- *metav1.PartialObjectMetadata) *cache.ResourceEventHandlerFuncs
	}{
		// scenario 1
		{
			name: "scenario 1: test if adding an object triggers AddFunc",
			ns:   "ns-foo",
			gvr:  schema.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "deployments"},
			trigger: func(gvr schema.GroupVersionResource, ns string, fakeClient *fake.FakeMetadataClient, _ *metav1.PartialObjectMetadata) *metav1.PartialObjectMetadata {
				testObject := newPartialObjectMetadata("extensions/v1beta1", "Deployment", "ns-foo", "name-foo")
				createdObj, err := fakeClient.Resource(gvr).Namespace(ns).(fake.MetadataClient).CreateFake(testObject, metav1.CreateOptions{})
				if err != nil {
					t.Error(err)
				}
				return createdObj
			},
			handler: func(rcvCh chan<- *metav1.PartialObjectMetadata) *cache.ResourceEventHandlerFuncs {
				return &cache.ResourceEventHandlerFuncs{
					AddFunc: func(obj interface{}) {
						rcvCh <- obj.(*metav1.PartialObjectMetadata)
					},
				}
			},
		},

		// scenario 2
		{
			name:        "scenario 2: tests if updating an object triggers UpdateFunc",
			ns:          "ns-foo",
			gvr:         schema.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "deployments"},
			existingObj: newPartialObjectMetadata("extensions/v1beta1", "Deployment", "ns-foo", "name-foo"),
			trigger: func(gvr schema.GroupVersionResource, ns string, fakeClient *fake.FakeMetadataClient, testObject *metav1.PartialObjectMetadata) *metav1.PartialObjectMetadata {
				if testObject.Annotations == nil {
					testObject.Annotations = make(map[string]string)
				}
				testObject.Annotations["test"] = "updatedName"
				updatedObj, err := fakeClient.Resource(gvr).Namespace(ns).(fake.MetadataClient).UpdateFake(testObject, metav1.UpdateOptions{})
				if err != nil {
					t.Error(err)
				}
				return updatedObj
			},
			handler: func(rcvCh chan<- *metav1.PartialObjectMetadata) *cache.ResourceEventHandlerFuncs {
				return &cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(old, updated interface{}) {
						rcvCh <- updated.(*metav1.PartialObjectMetadata)
					},
				}
			},
		},

		// scenario 3
		{
			name:        "scenario 3: test if deleting an object triggers DeleteFunc",
			ns:          "ns-foo",
			gvr:         schema.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "deployments"},
			existingObj: newPartialObjectMetadata("extensions/v1beta1", "Deployment", "ns-foo", "name-foo"),
			trigger: func(gvr schema.GroupVersionResource, ns string, fakeClient *fake.FakeMetadataClient, testObject *metav1.PartialObjectMetadata) *metav1.PartialObjectMetadata {
				err := fakeClient.Resource(gvr).Namespace(ns).Delete(context.TODO(), testObject.GetName(), metav1.DeleteOptions{})
				if err != nil {
					t.Error(err)
				}
				return testObject
			},
			handler: func(rcvCh chan<- *metav1.PartialObjectMetadata) *cache.ResourceEventHandlerFuncs {
				return &cache.ResourceEventHandlerFuncs{
					DeleteFunc: func(obj interface{}) {
						rcvCh <- obj.(*metav1.PartialObjectMetadata)
					},
				}
			},
		},
	}

	for _, ts := range scenarios {
		t.Run(ts.name, func(t *testing.T) {
			// test data
			timeout := time.Duration(3 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			scheme := runtime.NewScheme()
			metav1.AddMetaToScheme(scheme)
			informerReciveObjectCh := make(chan *metav1.PartialObjectMetadata, 1)
			objs := []runtime.Object{}
			if ts.existingObj != nil {
				objs = append(objs, ts.existingObj)
			}
			fakeClient := fake.NewSimpleMetadataClient(scheme, objs...)
			target := NewSharedInformerFactory(fakeClient, 0)

			// act
			informerListerForGvr := target.ForResource(ts.gvr)
			informerListerForGvr.Informer().AddEventHandler(ts.handler(informerReciveObjectCh))
			target.Start(ctx.Done())
			if synced := target.WaitForCacheSync(ctx.Done()); !synced[ts.gvr] {
				t.Fatalf("informer for %s hasn't synced", ts.gvr)
			}

			testObject := ts.trigger(ts.gvr, ts.ns, fakeClient, ts.existingObj)
			select {
			case objFromInformer := <-informerReciveObjectCh:
				if !equality.Semantic.DeepEqual(testObject, objFromInformer) {
					t.Fatalf("%v", diff.ObjectDiff(testObject, objFromInformer))
				}
			case <-ctx.Done():
				t.Errorf("tested informer haven't received an object, waited %v", timeout)
			}
		})
	}
}

// TestSpecificInformerStopOnListError tests that when an informer's
// lister errors out, the informer itself will shut down when
// stopOptions are set to stopOnListError and will not shut down when
// stopOptions are NOT set to stopOnListError
func TestSpecificInformerStopOnListError(t *testing.T) {
	scenarios := []struct {
		stopOnListError bool
	}{
		{
			stopOnListError: true,
		},
		{
			stopOnListError: false,
		},
	}

	for _, ts := range scenarios {
		timeout := time.Duration(3 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		stopOnListErrorFunc := func(error) bool {
			return ts.stopOnListError
		}
		testObject := newPartialObjectMetadata("extensions/v1beta1", "Deployment", "ns-foo", "name-foo")
		scheme := runtime.NewScheme()
		metav1.AddMetaToScheme(scheme)
		fakeClient := fake.NewSimpleMetadataClient(scheme, []runtime.Object{testObject}...)
		gvr := schema.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "deployments"}
		listReactor := func(a core.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("forced list error")
		}
		fakeClient.PrependReactor("*", "*", listReactor)
		target := NewSharedInformerFactoryWithOptions(fakeClient, 0, WithStopOnListError(stopOnListErrorFunc))

		// retrieve the informer for the resource forces the factory to create the informer.
		_ = target.ForResource(gvr)
		infCtx, _ := context.WithCancel(ctx)
		target.Start(infCtx.Done())
		info, ok := target.ForStoppableResource(gvr)
		if !ok {
			t.Errorf("Unable to retrieve done channel for gvr")
		}

		select {
		case <-info.Done:
			if !ts.stopOnListError {
				t.Errorf("informer should NOT have stopped when stopOnListError is false")
			}
		// timer must be shorter than the timeout or else it will close doneChannel
		// and the select statement will race.
		case <-time.NewTimer(2 * time.Second).C:
			if ts.stopOnListError {
				t.Errorf("informer SHOULD have stopped itself when stopOnListError is true, waited 2s")
			}
		}
	}
}

func newPartialObjectMetadata(apiVersion, kind, namespace, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiVersion,
			Kind:       kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
}
