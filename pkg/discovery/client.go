/*
Copyright 2019 Michael FIG <michael+k8s-copier@fig.org>

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

package discovery

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

type Client struct {
	client        discovery.DiscoveryInterface
	resourceLists []*metav1.APIResourceList
}

func NewForConfig(config *rest.Config) (*Client, error) {
	c := &Client{}

	client, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	c.client = client

	// TODO(mfig): We currently only detect resources once, maybe refresh.
	_, resourceLists, err := c.client.ServerGroupsAndResources()
	if err != nil {
		return nil, err
	}
	c.resourceLists = resourceLists

	return c, nil
}

func NewForConfigOrDie(config *rest.Config) *Client {
	c, err := NewForConfig(config)
	if err != nil {
		panic(err)
	}
	return c
}

func (c *Client) FindResources(spec string) ([]*schema.GroupVersionResource, error) {
	gvrp, gr := schema.ParseResourceArg(spec)

	// Use discovery to find the group/version.
	var gvrs []*schema.GroupVersionResource
	for _, resourceList := range c.resourceLists {
		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			// FIXME: Do something with the error.
			continue
		}
		var resourceSpec string
		if gvrp != nil && gv.Group == gvrp.Group && gv.Version == gvrp.Version {
			// Specified version.group matches exactly.  Use its resource.
			resourceSpec = gvrp.Resource
		} else if gr.Group == "" || gv.Group == gr.Group {
			// Match group, use its resource.
			resourceSpec = gr.Resource
		} else {
			// No match.
			continue
		}

		for _, resource := range resourceList.APIResources {
			if resource.Name == resourceSpec ||
				resource.SingularName == resourceSpec ||
				strings.EqualFold(resource.Kind, resourceSpec) {
				// Found a matching resource.
				gvrs = append(gvrs, &schema.GroupVersionResource{
					Group:    gv.Group,
					Resource: resource.Name,
					Version:  gv.Version,
				})
			}
		}
	}

	if len(gvrs) > 0 {
		return gvrs, nil
	}

	// Treat as legacy (v1)
	gvr := gr.WithVersion("v1")
	return []*schema.GroupVersionResource{&gvr}, nil
}
