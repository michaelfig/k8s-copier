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

	copier "github.com/michaelfig/k8s-copier/pkg/controller"
	logf "github.com/michaelfig/k8s-copier/pkg/logs"
	log "k8s.io/klog"

	ctrl "sigs.k8s.io/controller-runtime"
)

func RegisterAll(mgr ctrl.Manager) error {
	// TODO(mfig): Parse the resources supplied by --target= option.
	targets := []string{"federatedsecret"}
	namespaces := []string{"cloud"}

	ctx := context.TODO()
	c := copier.New(&ctx, mgr.GetConfig(), namespaces)

	for _, target := range targets {
		if err := c.AddTarget(target); err != nil {
			return err
		}
	}

	mgr.Add(c)
	return nil
}

func main() {
	logf.InitLogs(nil)

	stopCh := ctrl.SetupSignalHandler()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})

	if err != nil {
		log.Fatalf("error creating manager: %v", err)
	}

	if err := RegisterAll(mgr); err != nil {
		log.Fatalf("error registering controllers: %v", err)
	}

	if err := mgr.Start(stopCh); err != nil {
		log.Fatalf("error running manager: %v", err)
	}
}
