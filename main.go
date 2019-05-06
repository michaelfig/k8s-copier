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
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"

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

	resourceLists []*metav1.APIResourceList

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

// New returns a new controller. It sets up the informer handler
// functions for all the types it watches.
func New(ctx *context.Context, config *rest.Config, namespaces, targets []string) *Controller {
	ctrl := &Controller{Context: *ctx}
	ctrl.syncHandler = ctrl.processNextWorkItem
	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "copier")

	config.Host = "http://127.0.0.1:8001" // FIXME

	// TODO(mfig): We currently only detect resources once, maybe refresh.
	dclient := discovery.NewDiscoveryClientForConfigOrDie(config)
	resourceLists, err := dclient.ServerPreferredResources()
	if err != nil {
		panic(err)
	}
	ctrl.resourceLists = resourceLists

	gvrs := make([]schema.GroupVersionResource, len(targets))
	for i, target := range targets {
		gvrs[i] = *ctrl.FindResource(target)
	}

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

	ctrl.dynamicListers = make(map[schema.GroupVersionResource][]cache.GenericLister, len(targets))
	handler := &QueuingEventHandler{Queue: ctrl.queue}
	for _, gvr := range gvrs {
		informers := ctrl.AddInformers(&gvr, handler)
		for _, informer := range informers {
			ctrl.syncedFuncs = append(ctrl.syncedFuncs, informer.HasSynced)
		}
	}
	return ctrl
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
		return nil
	}

	// FIXME: Find out if we have an annotation, and add it to the
	// copier map if we do!
	log.Infof("Would process %s %s", namespace, name)

	gvr := c.FindResource("secret")
	if _, ok := c.dynamicListers[*gvr]; !ok {
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
	return c.Run(3, stopCh)
}

func (c *Controller) FindResource(spec string) *schema.GroupVersionResource {
	gvrp, gr := schema.ParseResourceArg(spec)
	if gvrp != nil {
		// Fully-specified.
		return gvrp
	}

	// Use discovery to find the group/version.
	for _, resourceList := range c.resourceLists {
		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			// FIXME: Do something with the error.
			continue
		}
		if gr.Group != "" && gv.Group != gr.Group {
			// Group was specified, and it's not the same.
			continue
		}
		for _, resource := range resourceList.APIResources {
			if resource.Name == gr.Resource ||
				resource.SingularName == gr.Resource ||
				strings.EqualFold(resource.Kind, gr.Resource) {
				// Found a matching resource.
				return &schema.GroupVersionResource{
					Group:    gv.Group,
					Resource: resource.Name,
					Version:  gv.Version,
				}
			}
		}
	}

	// Treat as legacy (v1)
	gvr := gr.WithVersion("v1")
	return &gvr
}

func RegisterAll(mgr ctrl.Manager) error {
	// TODO(mfig): Parse the resources supplied by --target= option.
	targets := []string{"federatedsecret"}
	namespaces := []string{"cloud"}
	ctx := context.TODO()
	c := New(&ctx, mgr.GetConfig(), namespaces, targets)

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
