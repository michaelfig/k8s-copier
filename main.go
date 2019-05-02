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
	"fmt"
	"sync"
	"github.com/michaelfig/k8s-copier/logf"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	log "k8s.io/klog"
)

type Controller struct {
	Context				context.Context

	dynamicListers      map[schema.GroupVersionResource]cache.GenericLister
	queue               workqueue.RateLimitingInterface
	syncedFuncs			[]cache.InformerSynced
	workerWg			sync.WaitGroup
}

// New returns a new controller. It sets up the informer handler
// functions for all the types it watches.
func New(ctx *context.Context, namespaces []string, targets []schema.GroupVersionResource) *Controller {
	ctrl := &Controller{Context: ctx}
	ctrl.syncHandler = ctrl.processNextWorkItem
	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter()

	dynclient := dynamic.NewForConfigOrDie()
	factory = dynamicinformer.NewDynamicSharedInformerFactory(dynclient, 0)

	
	for _, gvr = range(targets) {
		target := factory.ForResource(gvr)
		ctrl.dynamicListers[gvr] = target.Lister()
		target.Informer().AddEventHandler()
		ctrl.syncedFuncs = append(ctrl.syncedFuncs, target.Informer().HasSynced)
	}
	return ctrl
}

func (c *Controller) Run(workers int, stopCh <-chan struct{}) error {
	ctx, cancel := context.WithCancel(c.ctx)
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
	log := logf.FromContext(ctx)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Error(err, "invalid resource key")
		return nil
	}

	crt, err := c.certificateLister.Certificates(namespace).Get(name)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			c.scheduledWorkQueue.Forget(key)
			log.Error(err, "certificate in work queue no longer exists")
			return nil
		}

		return err
	}

	ctx = logf.NewContext(ctx, logf.WithResource(log, crt))
	return c.Sync(ctx, crt)
}

func main() {
	gvrs := []schema.GroupVersionResource{
		{Group: "types.federation.k8s.io", Version: "v1alpha1", Resource: "federatedsecrets"}
	}
	namespaces := []string{}
	ctrl := New(context.TODO(), namespaces, gvrs)
	ctrl.Run(3);
}
