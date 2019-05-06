/*
Copyright 2019 Michael FIG <michael+k8s-copier@fig.org>
Copyright 2018

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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/michaelfig/k8s-copier/pkg/discovery"
	logf "github.com/michaelfig/k8s-copier/pkg/logs"
	log "k8s.io/klog"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

var (
	KeyFunc = cache.DeletionHandlingMetaNamespaceKeyFunc
)

type Controller struct {
	Context context.Context

	discovery      *discovery.Client
	dynamicListers map[schema.GroupVersionResource][]cache.GenericLister
	factories      []dynamicinformer.DynamicSharedInformerFactory
	queue          workqueue.RateLimitingInterface
	syncedFuncs    []cache.InformerSynced
	syncHandler    func(context.Context, string, <-chan struct{}) error
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
	// myMeta := obj.(meta.Interface)
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

// New returns a new controller.
func New(ctx *context.Context, config *rest.Config, namespaces []string) *Controller {
	ctrl := &Controller{Context: *ctx}
	ctrl.dynamicListers = make(map[schema.GroupVersionResource][]cache.GenericLister)
	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "copier")
	ctrl.syncHandler = ctrl.processNextWorkItem

	dynclient := dynamic.NewForConfigOrDie(config)
	if len(namespaces) == 0 {
		// Create a single all-namespaces factory.
		ctrl.factories = []dynamicinformer.DynamicSharedInformerFactory{
			dynamicinformer.NewDynamicSharedInformerFactory(dynclient, 0),
		}
	} else {
		// Create a factory per namespace.
		ctrl.factories = make([]dynamicinformer.DynamicSharedInformerFactory, len(namespaces))
		for i, namespace := range namespaces {
			ctrl.factories[i] = dynamicinformer.NewFilteredDynamicSharedInformerFactory(
				dynclient,
				0,
				namespace,
				nil,
			)
		}
	}

	ctrl.discovery = discovery.NewForConfigOrDie(config)
	return ctrl
}

func (c *Controller) AddTarget(target string) error {
	gvrs, err := c.discovery.FindResources(target)
	if err != nil {
		return err
	}

	handler := &QueuingEventHandler{Queue: c.queue}
	for _, gvr := range gvrs {
		informers := c.AddInformers(gvr, handler)
		for _, informer := range informers {
			c.syncedFuncs = append(c.syncedFuncs, informer.HasSynced)
		}
	}
	return nil
}

func (c *Controller) AddInformers(gvr *schema.GroupVersionResource, handler cache.ResourceEventHandler) []cache.SharedInformer {
	c.dynamicListers[*gvr] = make([]cache.GenericLister, len(c.factories))
	informers := make([]cache.SharedInformer, len(c.factories))
	for i, factory := range c.factories {
		target := factory.ForResource(*gvr)
		c.dynamicListers[*gvr][i] = target.Lister()
		informers[i] = target.Informer()
		informers[i].AddEventHandler(handler)
	}
	return informers
}

func (c *Controller) Run(workers int, stopCh <-chan struct{}) error {
	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()

	log.Info("starting control loop")
	for _, factory := range c.factories {
		factory.Start(stopCh)
	}

	// wait for all the informer caches we depend to sync
	if !cache.WaitForCacheSync(stopCh, c.syncedFuncs...) {
		return fmt.Errorf("error waiting for informer caches to sync")
	}

	log.Info("synced all caches for control loop")

	for i := 0; i < workers; i++ {
		c.workerWg.Add(1)
		// TODO (@munnerz): make time.Second duration configurable
		go wait.Until(func() { c.worker(ctx, stopCh) }, time.Second, stopCh)
	}

	<-stopCh
	log.V(logf.DebugLevel).Info("shutting down queue as workqueue signaled shutdown")
	c.queue.ShutDown()
	log.V(logf.DebugLevel).Info("waiting for workers to exit...")
	c.workerWg.Wait()
	log.V(logf.DebugLevel).Info("workers exited")
	return nil
}

func (c *Controller) worker(ctx context.Context, stopCh <-chan struct{}) {
	// log := logf.FromContext(ctx)
	defer c.workerWg.Done()
	log.V(logf.DebugLevel).Info("starting worker")
	for {
		obj, shutdown := c.queue.Get()
		if shutdown {
			break
		}

		log.V(logf.DebugLevel).Infof("got obj %v", obj)
		var key string
		// use an inlined function so we can use defer
		func() {
			defer c.queue.Done(obj)
			var ok bool
			if key, ok = obj.(string); !ok {
				return
			}
			// log := log.WithValues("key", key)
			log.Infof("syncing resource %s", key)
			if err := c.syncHandler(ctx, key, stopCh); err != nil {
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

func (c *Controller) processNextWorkItem(ctx context.Context, key string, stopCh <-chan struct{}) error {
	// log := logf.FromContext(ctx)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Error(err, "invalid resource key")
		return err
	}

	// FIXME: Find out if we have an annotation, and add it to the
	// copier map if we do!
	log.Infof("Would process %s %s", namespace, name)

	gvrs, err := c.discovery.FindResources("secret")
	if err != nil {
		log.Error(err, "error finding resource")
		return err
	}
	for _, gvr := range gvrs {
		if _, ok := c.dynamicListers[*gvr]; ok {
			continue
		}
		// Create new informers for the gvr we're watching.
		informers := c.AddInformers(gvr, &QueuingEventHandler{Queue: c.queue})
		for _, informer := range informers {
			informer.Run(stopCh)
		}
	}

	// ctx = logf.NewContext(ctx, logf.WithResource(log, crt))
	// return c.Sync(ctx, crt)
	return nil
}

func (c *Controller) Start(stopCh <-chan struct{}) error {
	// TODO: Make configurable.
	numWorkers := 3
	return c.Run(numWorkers, stopCh)
}
