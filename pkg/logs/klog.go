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

package logs

import (
	"flag"
	"log"
	"os"

	"k8s.io/klog"
)

// InitLogs initializes logs the way we want for kubernetes.
func InitLogs(fs *flag.FlagSet) {
	if fs == nil {
		fs = flag.CommandLine
	}
	klog.InitFlags(fs)
	fs.Set("logtostderr", "true")

	log.SetOutput(os.Stderr)
}
