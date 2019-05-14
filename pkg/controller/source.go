package controller

import (
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	log "k8s.io/klog"
)

type ResourceSource struct {
	Control *Controller
	Spec    *Resource
	Path    []string
}

type JSONSource struct {
	Data interface{}
}

type Source interface {
	GetData() (interface{}, error)
	Register(*Controller, <-chan struct{}, *Rule, *ResourceInstance) error
}

func ParseSource(source, defaultNamespace string) (Source, error) {
	if jsonData := strings.TrimPrefix(source, "json:"); jsonData != source {
		var data interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			return nil, err
		}
		return &JSONSource{
			Data: data,
		}, nil
	}
	split := strings.SplitN(source, ":", 3)
	nsplit := strings.SplitN(split[1], "/", 2)
	var res *Resource
	if len(nsplit) > 1 {
		res = &Resource{
			Kind:      split[0],
			Namespace: nsplit[0],
			Name:      nsplit[1],
		}
	} else {
		res = &Resource{
			Kind:      split[0],
			Namespace: defaultNamespace,
			Name:      nsplit[0],
		}
	}
	return &ResourceSource{
		Spec: res,
		Path: strings.Split(split[2], "."),
	}, nil
}

func (src *JSONSource) GetData() (interface{}, error) {
	return src.Data, nil
}

func (src *JSONSource) Register(c *Controller, stopCh <-chan struct{}, rule *Rule, target *ResourceInstance) error {
	return nil
}

func (src *ResourceSource) GetData() (interface{}, error) {
	// FIXME: Return the actual data for the source path.
	source, err := src.Control.FindResourceInstance(src.Spec)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, nil
	}

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(source.Object)
	if err != nil {
		return nil, err
	}

	// Navigate the path.
	result, found, err := unstructured.NestedFieldCopy(obj, src.Path...)
	if err != nil || !found {
		return nil, err
	}

	return result, nil
}

func (src *ResourceSource) Register(c *Controller, stopCh <-chan struct{}, rule *Rule, target *ResourceInstance) error {
	// Link in the resource spec.
	rule.Target = target.Resource
	src.Control = c

	gvrs, err := c.discovery.FindResources(src.Spec.Kind)
	if err != nil {
		log.Error(err, "error finding resource", src.Spec.Kind)
		return err
	}

	// We can mark the target for this source.
	for _, gvr := range gvrs {
		func() {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			if _, ok := c.sources[*gvr]; !ok {
				c.sources[*gvr] = make(map[string]map[Resource]*Rule)
			}
			key := src.Spec.Key()
			if targets := c.sources[*gvr][key]; targets != nil {
				targets[*rule.Target] = rule
			} else {
				c.sources[*gvr][key] = make(map[Resource]*Rule)
				c.sources[*gvr][key][*rule.Target] = rule
			}
		}()
		// Create new informers for the gvr we're watching.
		informers := c.AddInformers(gvr, &QueuingEventHandler{
			Queue: c.queue,
			GVR:   gvr,
		})
		for _, informer := range informers {
			informer.Run(stopCh)
		}
	}

	// Try invoking the rule.
	return rule.Apply(c, rule, target)
}
