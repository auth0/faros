/*
Copyright 2018 Pusher Ltd.

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

package flags

import (
	"fmt"
	"strings"

	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// FlagSet contains faros flags that are needed in multiple packages
	FlagSet *flag.FlagSet

	// Namespace is the namespace for the controller to be restricted to
	Namespace string

	// CreateOwnerReference links GitTrack and GitTrackObjects for easier cleanup, defaults to true
	CreateOwnerReference bool

	// ignoredResources is a list of Kubernets kinds to ignore when reconciling
	ignoredResources []string
)

func init() {
	FlagSet = flag.NewFlagSet("faros", flag.PanicOnError)
	FlagSet.StringVar(&Namespace, "namespace", "", "Only manage GitTrack resources in given namespace")
	FlagSet.BoolVar(&CreateOwnerReference, "create-owner-reference", true, "Link child resources to GitTrack resource for easier cleanup")
	FlagSet.StringSliceVar(&ignoredResources, "ignore-resource", []string{}, "Ignore resources of these kinds found in repositories, specified in <resource>.<group>/<version> format eg jobs.batch/v1")
}

// ParseIgnoredResources attempts to parse the ignore-resource flag value and
// create a set of GroupVersionResources from the slice
func ParseIgnoredResources() (map[schema.GroupVersionResource]interface{}, error) {
	gvrs := make(map[schema.GroupVersionResource]interface{})
	for _, ignored := range ignoredResources {
		if !strings.Contains(ignored, ".") || !strings.Contains(ignored, "/") {
			return nil, fmt.Errorf("%s is invalid, should be of format <resource>.<group>/<version>", ignored)
		}
		split := strings.SplitN(ignored, ".", 2)
		gv, err := schema.ParseGroupVersion(split[1])
		if err != nil {
			return nil, fmt.Errorf("unable to parse group version %s: %v", split[1], err)
		}
		gvr := schema.GroupVersionResource{
			Group:    gv.Group,
			Version:  gv.Version,
			Resource: split[0],
		}
		gvrs[gvr] = nil
	}
	return gvrs, nil
}
