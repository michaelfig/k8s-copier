/*
Copyright 2019 Michael FIG <michael+k8s-copier@fig.org>
Copyright 2018 The Jetstack cert-manager contributors.

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
	"strings"
	"sync"
	"time"

	"github.com/michaelfig/k8s-copier/pkg/discovery"
	logf "github.com/michaelfig/k8s-copier/pkg/logs"
	log "k8s.io/klog"

	"k8s.io/apimachinery/pkg/api/meta"
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

	discovery         *discovery.Client
	dynamicListers    map[schema.GroupVersionResource][]cache.GenericLister
	factories         []dynamicinformer.DynamicSharedInformerFactory
	sourceQueue       workqueue.RateLimitingInterface
	sourceSyncHandler func(context.Context, string, <-chan struct{}) error
	sourceWorkerWg    sync.WaitGroup
	syncedFuncs       []cache.InformerSynced
	targetAnnotations map[string]map[string]string
	targetQueue       workqueue.RateLimitingInterface
	targetSyncHandler func(context.Context, string, <-chan struct{}) error
	targetWorkerWg    sync.WaitGroup
}

type QueuingEventHandler struct {
	Annotations map[string]map[string]string
	GVR         *schema.GroupVersionResource
	Queue       workqueue.RateLimitingInterface
}

func (q *QueuingEventHandler) Enqueue(obj interface{}) {
	key, err := KeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	myMeta, err := meta.Accessor(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	fullKey := q.GVR.Resource
	if q.GVR.Group != "" {
		fullKey += "." + q.GVR.Version + "." + q.GVR.Group
	}
	fullKey += ":" + key
	if q.Annotations != nil {
		q.Annotations[fullKey] = myMeta.GetAnnotations()
	}
	q.Queue.Add(fullKey)
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
	ctrl.targetAnnotations = make(map[string]map[string]string)
	ctrl.dynamicListers = make(map[schema.GroupVersionResource][]cache.GenericLister)
	ctrl.sourceQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "source")
	ctrl.targetQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "target")
	ctrl.sourceSyncHandler = ctrl.processNextSourceWorkItem
	ctrl.targetSyncHandler = ctrl.processNextTargetWorkItem

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

	for _, gvr := range gvrs {
		handler := &QueuingEventHandler{
			Queue:       c.targetQueue,
			GVR:         gvr,
			Annotations: c.targetAnnotations,
		}
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
		c.targetWorkerWg.Add(1)
		go wait.Until(func() { c.targetWorker(ctx, stopCh) }, time.Second, stopCh)
	}

	for i := 0; i < workers; i++ {
		c.sourceWorkerWg.Add(1)
		go wait.Until(func() { c.sourceWorker(ctx, stopCh) }, time.Second, stopCh)
	}

	<-stopCh
	log.V(logf.DebugLevel).Info("shutting down queues as workqueue signaled shutdown")
	c.sourceQueue.ShutDown()
	c.targetQueue.ShutDown()
	log.V(logf.DebugLevel).Info("waiting for workers to exit...")
	c.sourceWorkerWg.Wait()
	c.targetWorkerWg.Wait()
	log.V(logf.DebugLevel).Info("workers exited")
	return nil
}

func (c *Controller) targetWorker(ctx context.Context, stopCh <-chan struct{}) {
	// log := logf.FromContext(ctx)
	defer c.targetWorkerWg.Done()
	log.V(logf.DebugLevel).Info("starting target worker")
	for {
		obj, shutdown := c.targetQueue.Get()
		if shutdown {
			break
		}

		log.V(logf.DebugLevel).Infof("got target obj %v", obj)
		var key string
		// use an inlined function so we can use defer
		func() {
			defer c.targetQueue.Done(obj)
			var ok bool
			if key, ok = obj.(string); !ok {
				return
			}
			// log := log.WithValues("key", key)
			log.Infof("syncing target resource %s", key)
			if err := c.targetSyncHandler(ctx, key, stopCh); err != nil {
				log.Error(err, "re-queuing item  due to error processing")
				c.targetQueue.AddRateLimited(obj)
				return
			}
			log.Info("finished processing target work item")
			c.targetQueue.Forget(obj)
		}()
	}
	log.V(logf.DebugLevel).Info("exiting target worker loop")
}

func (c *Controller) sourceWorker(ctx context.Context, stopCh <-chan struct{}) {
	// log := logf.FromContext(ctx)
	defer c.sourceWorkerWg.Done()
	log.V(logf.DebugLevel).Info("starting source worker")
	for {
		obj, shutdown := c.sourceQueue.Get()
		if shutdown {
			break
		}

		log.V(logf.DebugLevel).Infof("got source obj %v", obj)
		var key string
		// use an inlined function so we can use defer
		func() {
			defer c.sourceQueue.Done(obj)
			var ok bool
			if key, ok = obj.(string); !ok {
				return
			}
			// log := log.WithValues("key", key)
			log.Infof("syncing source resource %s", key)
			if err := c.sourceSyncHandler(ctx, key, stopCh); err != nil {
				log.Error(err, "re-queuing item  due to error processing")
				c.sourceQueue.AddRateLimited(obj)
				return
			}
			log.Info("finished processing source work item")
			c.sourceQueue.Forget(obj)
		}()
	}
	log.V(logf.DebugLevel).Info("exiting source worker loop")
}

func (c *Controller) processNextTargetWorkItem(ctx context.Context, key string, stopCh <-chan struct{}) error {
	// log := logf.FromContext(ctx)
	splits := strings.SplitN(key, ":", 2)
	gvrs, err := c.discovery.FindResources(splits[0])
	if err != nil {
		log.Error(err, ": cannot find resource ", splits[0])
		return err
	}

	// FIXME: Parse annotations to target handlers.
	log.Infof("Would process %s annotations %s", key, c.targetAnnotations[key])
	gvrs, err = c.discovery.FindResources("secret")
	if err != nil {
		log.Error(err, "error finding resource")
		return err
	}

	namespace, name := "cloud", "cloud-azure-config-file"
	for _, gvr := range gvrs {
		if dls := c.dynamicListers[*gvr]; dls != nil {
			// We are listing it already.
			obj, err := FindInListers(dls, namespace, name)
			if err != nil {
				log.Error(err, " finding ", namespace, name)
			}
			if obj != nil {
				log.Infof("found object %v", obj)
			}
			continue
		}
		// Create new informers for the gvr we're watching.
		// Don't watch Annotations.
		informers := c.AddInformers(gvr, &QueuingEventHandler{
			Queue: c.sourceQueue,
			GVR:   gvr,
		})
		for _, informer := range informers {
			informer.Run(stopCh)
		}
	}

	// ctx = logf.NewContext(ctx, logf.WithResource(log, crt))
	// return c.Sync(ctx, crt)
	return nil
}

func FindInListers(dls []cache.GenericLister, namespace, name string) (interface{}, error) {
	for _, dl := range dls {
		if l := dl.ByNamespace(namespace); l != nil {
			obj, err := l.Get(name)
			if err != nil {
				log.Error(err, "error looking up object")
				continue
			}
			if obj != nil {
				return obj, nil
			}
		}
	}
	return nil, nil
}

func (c *Controller) processNextSourceWorkItem(ctx context.Context, key string, stopCh <-chan struct{}) error {
	// log := logf.FromContext(ctx)

	// FIXME: Update the targets accordingly.
	log.Infof("Would check %s for targets", key)
	return nil
}

func (c *Controller) Start(stopCh <-chan struct{}) error {
	// TODO: Make configurable.
	numWorkers := 3
	return c.Run(numWorkers, stopCh)
}
