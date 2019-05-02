/*
Copyright 2019 Michael FIG <michael@fig.org>

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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/michaelfig/k8s-copier/logf"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	log "k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	KeyFunc = cache.DeletionHandlingMetaNamespaceKeyFunc
)

type Controller struct {
	Context context.Context

	dynamicListers map[schema.GroupVersionResource][]cache.GenericLister
	queue          workqueue.RateLimitingInterface
	syncedFuncs    []cache.InformerSynced
	syncHandler    func(context.Context, string) error
	workerWg       sync.WaitGroup
}

type QueuingEventHandler struct {
	Queue workqueue.RateLimitingInterface
}

func (q *QueuingEventHandler) Enqueue(obj interface{}) {
	key, err := KeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	q.Queue.Add(key)
}

func (q *QueuingEventHandler) OnAdd(obj interface{}) {
	q.Enqueue(obj)
}

func (q *QueuingEventHandler) OnUpdate(old, new interface{}) {
	if reflect.DeepEqual(old, new) {
		return
	}
	q.Enqueue(new)
}

func (q *QueuingEventHandler) OnDelete(obj interface{}) {
	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if ok {
		obj = tombstone.Obj
	}
	q.Enqueue(obj)
}

// New returns a new controller. It sets up the informer handler
// functions for all the types it watches.
func New(ctx *context.Context, config *rest.Config, namespaces []string, targets []schema.GroupVersionResource) *Controller {
	ctrl := &Controller{Context: *ctx}
	ctrl.syncHandler = ctrl.processNextWorkItem
	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "copier")

	dynclient := dynamic.NewForConfigOrDie(config)
	var factories []dynamicinformer.DynamicSharedInformerFactory
	if len(namespaces) == 0 {
		// Create a single all-namespaces factory.
		factories = []dynamicinformer.DynamicSharedInformerFactory{
			dynamicinformer.NewDynamicSharedInformerFactory(dynclient, 0),
		}
	} else {
		// Create a factory per namespace.
		factories = make([]dynamicinformer.DynamicSharedInformerFactory, len(namespaces))
		for i, namespace := range namespaces {
			factories[i] = dynamicinformer.NewFilteredDynamicSharedInformerFactory(
				dynclient,
				0,
				namespace,
				nil,
			)
		}
	}

	ctrl.dynamicListers = make(map[schema.GroupVersionResource][]cache.GenericLister, len(targets))
	for _, gvr := range targets {
		ctrl.dynamicListers[gvr] = make([]cache.GenericLister, len(factories))
		for _, factory := range factories {
			target := factory.ForResource(gvr)
			ctrl.dynamicListers[gvr] = append(ctrl.dynamicListers[gvr], target.Lister())
			target.Informer().AddEventHandler(&QueuingEventHandler{Queue: ctrl.queue})
			ctrl.syncedFuncs = append(ctrl.syncedFuncs, target.Informer().HasSynced)
		}
	}
	return ctrl
}

func (c *Controller) Run(workers int, stopCh <-chan struct{}) error {
	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()

	log.Info("starting control loop")
	// wait for all the informer caches we depend to sync
	if !cache.WaitForCacheSync(stopCh, c.syncedFuncs...) {
		return fmt.Errorf("error waiting for informer caches to sync")
	}

	log.Info("synced all caches for control loop")

	for i := 0; i < workers; i++ {
		c.workerWg.Add(1)
		// TODO (@munnerz): make time.Second duration configurable
		go wait.Until(func() { c.worker(ctx) }, time.Second, stopCh)
	}

	<-stopCh
	log.V(logf.DebugLevel).Info("shutting down queue as workqueue signaled shutdown")
	c.queue.ShutDown()
	log.V(logf.DebugLevel).Info("waiting for workers to exit...")
	c.workerWg.Wait()
	log.V(logf.DebugLevel).Info("workers exited")
	return nil
}

func (c *Controller) worker(ctx context.Context) {
	// log := logf.FromContext(ctx)
	defer c.workerWg.Done()
	log.V(logf.DebugLevel).Info("starting worker")
	for {
		obj, shutdown := c.queue.Get()
		if shutdown {
			break
		}

		var key string
		// use an inlined function so we can use defer
		func() {
			defer c.queue.Done(obj)
			var ok bool
			if key, ok = obj.(string); !ok {
				return
			}
			// log := log.WithValues("key", key)
			log.Info("syncing resource")
			if err := c.syncHandler(ctx, key); err != nil {
				log.Error(err, "re-queuing item  due to error processing")
				c.queue.AddRateLimited(obj)
				return
			}
			log.Info("finished processing work item")
			c.queue.Forget(obj)
		}()
	}
	log.V(logf.DebugLevel).Info("exiting worker loop")
}

func (c *Controller) processNextWorkItem(ctx context.Context, key string) error {
	// log := logf.FromContext(ctx)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Error(err, "invalid resource key")
		return nil
	}

	log.Info("Would process", namespace, name)
	// ctx = logf.NewContext(ctx, logf.WithResource(log, crt))
	// return c.Sync(ctx, crt)
	return nil
}

func (c *Controller) Start(stopCh <-chan struct{}) error {
	return c.Run(3, stopCh)
}

func RegisterAll(mgr ctrl.Manager) error {
	gvrs := []schema.GroupVersionResource{
		{Group: "types.federation.k8s.io", Version: "v1alpha1", Resource: "federatedsecrets"},
	}
	namespaces := []string{}
	ctx := context.TODO()
	c := New(&ctx, mgr.GetConfig(), namespaces, gvrs)

	mgr.Add(c)
	return nil
}

// InitLogs initializes logs the way we want for kubernetes.
func InitLogs(fs *flag.FlagSet) {
	if fs == nil {
		fs = flag.CommandLine
	}
	klog.InitFlags(fs)
	fs.Set("logtostderr", "true")

	log.SetOutput(os.Stdout)
}

func main() {
	InitLogs(nil)

	stopCh := ctrl.SetupSignalHandler()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})

	if err != nil {
		klog.Fatalf("error creating manager: %v", err)
	}

	// TODO(directxman12): enabled controllers for separate injectors?
	if err := RegisterAll(mgr); err != nil {
		klog.Fatalf("error registering controllers: %v", err)
	}

	if err := mgr.Start(stopCh); err != nil {
		klog.Fatalf("error running manager: %v", err)
	}
}
