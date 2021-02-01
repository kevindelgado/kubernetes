/*
Copyright 2017 The Kubernetes Authors.

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

package cache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/clock"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	fcache "k8s.io/client-go/tools/cache/testing"
)

type testListener struct {
	lock              sync.RWMutex
	resyncPeriod      time.Duration
	expectedItemNames sets.String
	receivedItemNames []string
	name              string
}

func newTestListener(name string, resyncPeriod time.Duration, expected ...string) *testListener {
	l := &testListener{
		resyncPeriod:      resyncPeriod,
		expectedItemNames: sets.NewString(expected...),
		name:              name,
	}
	return l
}

func (l *testListener) OnAdd(obj interface{}) {
	l.handle(obj)
}

func (l *testListener) OnUpdate(old, new interface{}) {
	l.handle(new)
}

func (l *testListener) OnDelete(obj interface{}) {
}

func (l *testListener) handle(obj interface{}) {
	key, _ := MetaNamespaceKeyFunc(obj)
	fmt.Printf("%s: handle: %v\n", l.name, key)
	l.lock.Lock()
	defer l.lock.Unlock()

	objectMeta, _ := meta.Accessor(obj)
	l.receivedItemNames = append(l.receivedItemNames, objectMeta.GetName())
}

func (l *testListener) ok() bool {
	fmt.Println("polling")
	err := wait.PollImmediate(100*time.Millisecond, 2*time.Second, func() (bool, error) {
		if l.satisfiedExpectations() {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return false
	}

	// wait just a bit to allow any unexpected stragglers to come in
	fmt.Println("sleeping")
	time.Sleep(1 * time.Second)
	fmt.Println("final check")
	return l.satisfiedExpectations()
}

func (l *testListener) satisfiedExpectations() bool {
	l.lock.RLock()
	defer l.lock.RUnlock()

	return sets.NewString(l.receivedItemNames...).Equal(l.expectedItemNames)
}

func TestListenerResyncPeriods(t *testing.T) {
	// source simulates an apiserver object endpoint.
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod2"}})

	// create the shared informer and resync every 1s
	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)

	clock := clock.NewFakeClock(time.Now())
	informer.clock = clock
	informer.processor.clock = clock

	// listener 1, never resync
	listener1 := newTestListener("listener1", 0, "pod1", "pod2")
	informer.AddEventHandlerWithResyncPeriod(listener1, listener1.resyncPeriod)

	// listener 2, resync every 2s
	listener2 := newTestListener("listener2", 2*time.Second, "pod1", "pod2")
	informer.AddEventHandlerWithResyncPeriod(listener2, listener2.resyncPeriod)

	// listener 3, resync every 3s
	listener3 := newTestListener("listener3", 3*time.Second, "pod1", "pod2")
	informer.AddEventHandlerWithResyncPeriod(listener3, listener3.resyncPeriod)
	listeners := []*testListener{listener1, listener2, listener3}

	stop := make(chan struct{})
	defer close(stop)

	go informer.Run(stop)

	// ensure all listeners got the initial List
	for _, listener := range listeners {
		if !listener.ok() {
			t.Errorf("%s: expected %v, got %v", listener.name, listener.expectedItemNames, listener.receivedItemNames)
		}
	}

	// reset
	for _, listener := range listeners {
		listener.receivedItemNames = []string{}
	}

	// advance so listener2 gets a resync
	clock.Step(2 * time.Second)

	// make sure listener2 got the resync
	if !listener2.ok() {
		t.Errorf("%s: expected %v, got %v", listener2.name, listener2.expectedItemNames, listener2.receivedItemNames)
	}

	// wait a bit to give errant items a chance to go to 1 and 3
	time.Sleep(1 * time.Second)

	// make sure listeners 1 and 3 got nothing
	if len(listener1.receivedItemNames) != 0 {
		t.Errorf("listener1: should not have resynced (got %d)", len(listener1.receivedItemNames))
	}
	if len(listener3.receivedItemNames) != 0 {
		t.Errorf("listener3: should not have resynced (got %d)", len(listener3.receivedItemNames))
	}

	// reset
	for _, listener := range listeners {
		listener.receivedItemNames = []string{}
	}

	// advance so listener3 gets a resync
	clock.Step(1 * time.Second)

	// make sure listener3 got the resync
	if !listener3.ok() {
		t.Errorf("%s: expected %v, got %v", listener3.name, listener3.expectedItemNames, listener3.receivedItemNames)
	}

	// wait a bit to give errant items a chance to go to 1 and 2
	time.Sleep(1 * time.Second)

	// make sure listeners 1 and 2 got nothing
	if len(listener1.receivedItemNames) != 0 {
		t.Errorf("listener1: should not have resynced (got %d)", len(listener1.receivedItemNames))
	}
	if len(listener2.receivedItemNames) != 0 {
		t.Errorf("listener2: should not have resynced (got %d)", len(listener2.receivedItemNames))
	}
}

func TestResyncCheckPeriod(t *testing.T) {
	// source simulates an apiserver object endpoint.
	source := fcache.NewFakeControllerSource()

	// create the shared informer and resync every 12 hours
	informer := NewSharedInformer(source, &v1.Pod{}, 12*time.Hour).(*sharedIndexInformer)

	clock := clock.NewFakeClock(time.Now())
	informer.clock = clock
	informer.processor.clock = clock

	// listener 1, never resync
	listener1 := newTestListener("listener1", 0)
	informer.AddEventHandlerWithResyncPeriod(listener1, listener1.resyncPeriod)
	if e, a := 12*time.Hour, informer.resyncCheckPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := time.Duration(0), informer.processor.listeners[0].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}

	// listener 2, resync every minute
	listener2 := newTestListener("listener2", 1*time.Minute)
	informer.AddEventHandlerWithResyncPeriod(listener2, listener2.resyncPeriod)
	if e, a := 1*time.Minute, informer.resyncCheckPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := time.Duration(0), informer.processor.listeners[0].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := 1*time.Minute, informer.processor.listeners[1].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}

	// listener 3, resync every 55 seconds
	listener3 := newTestListener("listener3", 55*time.Second)
	informer.AddEventHandlerWithResyncPeriod(listener3, listener3.resyncPeriod)
	if e, a := 55*time.Second, informer.resyncCheckPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := time.Duration(0), informer.processor.listeners[0].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := 1*time.Minute, informer.processor.listeners[1].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := 55*time.Second, informer.processor.listeners[2].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}

	// listener 4, resync every 5 seconds
	listener4 := newTestListener("listener4", 5*time.Second)
	informer.AddEventHandlerWithResyncPeriod(listener4, listener4.resyncPeriod)
	if e, a := 5*time.Second, informer.resyncCheckPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := time.Duration(0), informer.processor.listeners[0].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := 1*time.Minute, informer.processor.listeners[1].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := 55*time.Second, informer.processor.listeners[2].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
	if e, a := 5*time.Second, informer.processor.listeners[3].resyncPeriod; e != a {
		t.Errorf("expected %d, got %d", e, a)
	}
}

// verify that https://github.com/kubernetes/kubernetes/issues/59822 is fixed
func TestSharedInformerInitializationRace(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)
	listener := newTestListener("raceListener", 0)

	stop := make(chan struct{})
	go informer.AddEventHandlerWithResyncPeriod(listener, listener.resyncPeriod)
	go informer.Run(stop)
	close(stop)
}

// TestSharedInformerWatchDisruption simulates a watch that was closed
// with updates to the store during that time. We ensure that handlers with
// resync and no resync see the expected state.
func TestSharedInformerWatchDisruption(t *testing.T) {
	// source simulates an apiserver object endpoint.
	source := fcache.NewFakeControllerSource()

	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", UID: "pod1", ResourceVersion: "1"}})
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod2", UID: "pod2", ResourceVersion: "2"}})

	// create the shared informer and resync every 1s
	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)

	clock := clock.NewFakeClock(time.Now())
	informer.clock = clock
	informer.processor.clock = clock

	// listener, never resync
	listenerNoResync := newTestListener("listenerNoResync", 0, "pod1", "pod2")
	informer.AddEventHandlerWithResyncPeriod(listenerNoResync, listenerNoResync.resyncPeriod)

	listenerResync := newTestListener("listenerResync", 1*time.Second, "pod1", "pod2")
	informer.AddEventHandlerWithResyncPeriod(listenerResync, listenerResync.resyncPeriod)
	listeners := []*testListener{listenerNoResync, listenerResync}

	stop := make(chan struct{})
	defer close(stop)

	go informer.Run(stop)

	for _, listener := range listeners {
		if !listener.ok() {
			t.Errorf("%s: expected %v, got %v", listener.name, listener.expectedItemNames, listener.receivedItemNames)
		}
	}

	// Add pod3, bump pod2 but don't broadcast it, so that the change will be seen only on relist
	source.AddDropWatch(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod3", UID: "pod3", ResourceVersion: "3"}})
	source.ModifyDropWatch(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod2", UID: "pod2", ResourceVersion: "4"}})

	// Ensure that nobody saw any changes
	for _, listener := range listeners {
		if !listener.ok() {
			t.Errorf("%s: expected %v, got %v", listener.name, listener.expectedItemNames, listener.receivedItemNames)
		}
	}

	for _, listener := range listeners {
		listener.receivedItemNames = []string{}
	}

	listenerNoResync.expectedItemNames = sets.NewString("pod2", "pod3")
	listenerResync.expectedItemNames = sets.NewString("pod1", "pod2", "pod3")

	// This calls shouldSync, which deletes noResync from the list of syncingListeners
	clock.Step(1 * time.Second)

	// Simulate a connection loss (or even just a too-old-watch)
	source.ResetWatch()

	for _, listener := range listeners {
		if !listener.ok() {
			t.Errorf("%s: expected %v, got %v", listener.name, listener.expectedItemNames, listener.receivedItemNames)
		}
	}
}

func TestSharedInformerErrorHandling(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})
	source.ListError = fmt.Errorf("Access Denied")

	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)

	errCh := make(chan error)
	_ = informer.SetWatchErrorHandler(func(_ *Reflector, err error) {
		errCh <- err
	})

	stop := make(chan struct{})
	go informer.Run(stop)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "Access Denied") {
			t.Errorf("Expected 'Access Denied' error. Actual: %v", err)
		}
	case <-time.After(time.Second):
		t.Errorf("Timeout waiting for error handler call")
	}
	close(stop)
}

// TestSharedInformerRunWithStopOptions runs an informer with StopOnError
// set to always return true. It tests that when the underlying reflector does reach a
// list error (upon trying to add an object) that the informer stops running before a
// given timout.
func TestSharedInformerRunWithStopOptions(t *testing.T) {
	// waitTime is how long to wait for the stopOnError=false case to pass.
	// It needs to just be long enough for the underlying reflector to make
	// its initial list call and error out.
	waitTime := time.Second
	table := []struct {
		stopOnError bool
	}{
		{true},
		{false},
	}
	for _, item := range table {
		source := fcache.NewFakeControllerSource()
		source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})
		source.ListError = fmt.Errorf("Access Denied")

		informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			// confirm the informer stops running when it hits a list error
			defer cancel()
			informer.RunWithStopOptions(ctx, StopOptions{
				StopOnError: func(err error) bool {
					return item.stopOnError
				},
			})
		}()

		select {
		case <-ctx.Done():
			if !item.stopOnError {
				t.Errorf("shared informer should NOT have stopped when stopOnError is false")
			}
		case <-time.After(waitTime):
			if item.stopOnError {
				t.Errorf("shared informer SHOULD have stopped itself when stopOnError is true, waited %s", waitTime.String())
			}
		}
	}
}

func TestSharedInformerRemoveHandler(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})
	source.ListError = fmt.Errorf("Access Denied")

	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second)

	handler1 := &ResourceEventHandlerFuncs{}
	informer.AddEventHandler(handler1)
	handler2 := &ResourceEventHandlerFuncs{}
	informer.AddEventHandler(handler2)

	if informer.EventHandlerCount() != 2 {
		t.Errorf("informer has %d registered handler, instead of 2", informer.EventHandlerCount())
	}

	if err := informer.RemoveEventHandler(handler2); err != nil {
		t.Errorf("removing of first pointer handler failed: %s", err)
	}
	if informer.EventHandlerCount() != 1 {
		t.Errorf("after removing handler informer has %d registered handler(s), instead of 1", informer.EventHandlerCount())
	}

	if err := informer.RemoveEventHandler(handler1); err != nil {
		t.Errorf("removing of second pointer handler failed: %s", err)
	}
	if informer.EventHandlerCount() != 0 {
		t.Errorf("informer still has registered handlers after removing both handlers")
	}
}

func TestSharedInformerRemoveHandlerFailure(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})
	source.ListError = fmt.Errorf("Access Denied")

	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second)

	handler1 := ResourceEventHandlerFuncs{}
	informer.AddEventHandler(handler1)
	handler2 := &ResourceEventHandlerFuncs{}
	informer.AddEventHandler(handler2)

	if informer.EventHandlerCount() != 2 {
		t.Errorf("informer has %d registered handler(s), instead of 2", informer.EventHandlerCount())
	}

	if err := informer.RemoveEventHandler(handler2); err != nil {
		t.Errorf("removing of pointer handler failed: %s", err)
	}
	if informer.EventHandlerCount() != 1 {
		t.Errorf("after removal informer has %d registered handler(s), instead of 1", informer.EventHandlerCount())
	}

	if err := informer.RemoveEventHandler(handler1); err == nil {
		t.Errorf("removing value handler did not fail")
	} else {
		if err.Error() != "Uncomparable handler {<nil> <nil> <nil>} is not removed" {
			t.Errorf("unexpected remove error: %s", err)
		}
	}
	if informer.EventHandlerCount() != 1 {
		t.Errorf("after failed removal informer has %d registered handler(s), instead of 1", informer.EventHandlerCount())
	}
}

func TestSharedInformerMultipleRegistration(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})
	source.ListError = fmt.Errorf("Access Denied")

	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second)

	handler1 := &ResourceEventHandlerFuncs{}
	informer.AddEventHandler(handler1)
	informer.AddEventHandler(handler1)

	if informer.EventHandlerCount() != 2 {
		t.Errorf("informer has %d registered handler(s), instead of 1", informer.EventHandlerCount())
	}

	if err := informer.RemoveEventHandler(handler1); err != nil {
		t.Errorf("removing of duplicate pointer handler failed: %s", err)
	}

	if informer.EventHandlerCount() != 0 {
		t.Errorf("informer still has a registered handler after removal of duplicate registrations")
	}
}

func TestStateSharedInformer(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})

	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)
	listener := newTestListener("listener", 0, "pod1")
	informer.AddEventHandlerWithResyncPeriod(listener, listener.resyncPeriod)

	if informer.IsStarted() {
		t.Errorf("informer already started after creation")
		return
	}
	if informer.IsStopped() {
		t.Errorf("informer already stopped after creation")
		return
	}
	stop := make(chan struct{})
	go informer.Run(stop)
	if !listener.ok() {
		t.Errorf("informer did not report initial objects")
		close(stop)
		return
	}

	if !informer.IsStarted() {
		t.Errorf("informer does not report to be started although handling events")
		close(stop)
		return
	}
	if informer.IsStopped() {
		t.Errorf("informer reports to be stopped although stop channel not closed")
		close(stop)
		return
	}

	close(stop)
	fmt.Println("sleeping")
	time.Sleep(1 * time.Second)

	if !informer.IsStopped() {
		t.Errorf("informer reports not to be stopped although stop channel closed")
		return
	}
	if !informer.IsStarted() {
		t.Errorf("informer reports not to be started after it has been started and stopped")
		return
	}
}

func TestRemoveOnStoppedSharedInformer(t *testing.T) {
	source := fcache.NewFakeControllerSource()
	source.Add(&v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}})

	informer := NewSharedInformer(source, &v1.Pod{}, 1*time.Second).(*sharedIndexInformer)
	listener := newTestListener("listener", 0, "pod1")
	informer.AddEventHandlerWithResyncPeriod(listener, listener.resyncPeriod)

	stop := make(chan struct{})
	go informer.Run(stop)
	close(stop)
	fmt.Println("sleeping")
	time.Sleep(1 * time.Second)

	if !informer.IsStopped() {
		t.Errorf("informer reports not to be stopped although stop channel closed")
		return
	}
	err := informer.RemoveEventHandler(listener)
	if err == nil {
		t.Errorf("informer removes handler on stopped informer")
		return
	}
	if !strings.HasSuffix(err.Error(), " is not removed from shared informer because it has stopped already") {
		t.Errorf("unexpected error for removing handler on stopped informer: %q", err)
	}
}
