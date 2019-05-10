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

	discovery      *discovery.Client
	dynamicListers map[schema.GroupVersionResource][]cache.GenericLister
	annotations    map[string]map[string]string
	factories      []dynamicinformer.DynamicSharedInformerFactory
	queue          workqueue.RateLimitingInterface
	syncHandler    func(context.Context, string, <-chan struct{}) error
	workerWg       sync.WaitGroup
	syncedFuncs    []cache.InformerSynced

	sources map[schema.GroupVersionResource]map[string]map[Target]bool
	targets map[schema.GroupVersionResource]bool
}

type Target struct {
	Resource *Resource
	Rule     *Rule
}

type Resource struct {
	Name      string
	Namespace string
	Kind      string
}

type ResourceInstance struct {
	Resource *Resource
	GVR      schema.GroupVersionResource
	Object   interface{}
}

type Rule struct {
	Name       string
	SourcePath string
	TargetPath string
}

func (r *Resource) Key() string {
	return r.Namespace + "/" + r.Name
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
	ctrl.annotations = make(map[string]map[string]string)
	ctrl.dynamicListers = make(map[schema.GroupVersionResource][]cache.GenericLister)
	ctrl.queue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "copier")
	ctrl.syncHandler = ctrl.processNextWorkItem

	ctrl.sources = make(map[schema.GroupVersionResource]map[string]map[Target]bool)
	ctrl.targets = make(map[schema.GroupVersionResource]bool)

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
			Queue:       c.queue,
			GVR:         gvr,
			Annotations: c.annotations,
		}
		c.targets[*gvr] = true
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
		go wait.Until(func() { c.worker(ctx, stopCh) }, time.Second, stopCh)
	}

	<-stopCh
	log.V(logf.DebugLevel).Info("shutting down queues as workqueue signaled shutdown")
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
				log.Error(err, "re-queuing item due to error processing")
				c.queue.AddRateLimited(obj)
				return
			}
			log.Info("finished processing work item")
			c.queue.Forget(obj)
		}()
	}
	log.V(logf.DebugLevel).Info("exiting worker loop")
}

func (c *Controller) FindResourceInstance(resource *Resource) (*ResourceInstance, error) {
	gvrs, err := c.discovery.FindResources(resource.Kind)
	if err != nil {
		return nil, err
	}
	if dls, ok := c.dynamicListers[*gvrs[0]]; ok {
		// We are listing it already.
		obj, err := FindInListers(dls, resource.Namespace, resource.Name)
		if err != nil {
			log.Error(err, "error looking up resource")
		}
		if obj != nil {
			return &ResourceInstance{
				Resource: resource,
				GVR:      *gvrs[0],
				Object:   obj,
			}, nil
		}
	}
	return nil, nil
}

func (c *Controller) InvokeRule(rule *Rule, target, source *ResourceInstance) error {
	log.Infof("would invoke rule %s on %s from %s", rule.Name, target.GVR, source.GVR)
	return nil
}

func (c *Controller) processNextWorkItem(ctx context.Context, key string, stopCh <-chan struct{}) error {
	// log := logf.FromContext(ctx)
	splits := strings.SplitN(key, ":", 2)
	nsplits := strings.SplitN(splits[1], "/", 2)
	self := &Resource{
		Name:      nsplits[1],
		Namespace: nsplits[0],
		Kind:      splits[0],
	}

	source, err := c.FindResourceInstance(self)
	if err != nil || source == nil {
		log.Error(err, ": cannot find self instance ", self)
		return err
	}
	if srcKeys, ok := c.sources[source.GVR]; ok {
		if targets, ok2 := srcKeys[splits[1]]; ok2 {
			for target := range targets {
				targetInstance, err := c.FindResourceInstance(target.Resource)
				if err != nil {
					log.Error(err, ": cannot find target instance ", target)
					continue
				}

				err = c.InvokeRule(target.Rule, targetInstance, source)
				if err != nil {
					log.Error(err, ": cannot invoke rule")
					continue
				}
			}
		}
	}

	target := source
	if _, ok := c.targets[target.GVR]; !ok {
		// Not a target.
		return nil
	}

	// FIXME: Parse annotations to target handlers.
	log.Infof("Would process %s annotations %s", key, c.annotations[key])
	sourceResource := &Resource{
		Name:      "cloud-azure-config-file",
		Namespace: "cloud",
		Kind:      "secret",
	}
	gvrs, err := c.discovery.FindResources(sourceResource.Kind)
	if err != nil {
		log.Error(err, "error finding resource", sourceResource.Kind)
		return err
	}

	targetObj := &Target{
		Resource: self,
		Rule:     &Rule{Name: "replace"},
	}

	// We can mark the target for this source.
	for _, gvr := range gvrs {
		if _, ok := c.sources[*gvr]; !ok {
			c.sources[*gvr] = make(map[string]map[Target]bool)
		}
		key := sourceResource.Key()
		if targets := c.sources[*gvr][key]; targets != nil {
			targets[*targetObj] = true
		} else {
			c.sources[*gvr][key] = make(map[Target]bool)
			c.sources[*gvr][key][*targetObj] = true
		}

		if _, ok := c.dynamicListers[*gvr]; !ok {
			// Create new informers for the gvr we're watching.
			informers := c.AddInformers(gvr, &QueuingEventHandler{
				Queue:       c.queue,
				GVR:         gvr,
				Annotations: c.annotations,
			})
			for _, informer := range informers {
				informer.Run(stopCh)
			}
		}
	}

	source, err = c.FindResourceInstance(sourceResource)
	if err != nil {
		return err
	}

	if source == nil {
		return nil
	}

	// We already have the source, so invoke the rule.
	return c.InvokeRule(targetObj.Rule, target, source)
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
