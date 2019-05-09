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
	"flag"

	logf "github.com/michaelfig/k8s-copier/pkg/logs"
	log "k8s.io/klog"
	logr "k8s.io/klog/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	logf.InitLogs(nil)
	defer log.Flush()
	logger := logr.New().WithName("k8s-copier")
	ctrl.SetLogger(logger)

	stopCh := ctrl.SetupSignalHandler()
	cmd := NewCommandCopierController(stopCh)
	cmd.Flags().AddGoFlagSet(flag.CommandLine)

	flag.CommandLine.Parse([]string{})
	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
