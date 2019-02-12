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

package utils

import (
	"context"

	farosv1alpha1 "github.com/pusher/faros/pkg/apis/faros/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	farosGroupVersion = "faros.pusher.com/v1alpha1"
)

// OwnerInNamespacePredicate filters events to check the owner of the event
// object is in the controller's namespace
type OwnerInNamespacePredicate struct {
	client client.Client
}

// Create returns true if the event object's owner is in the same namespace
func (p OwnerInNamespacePredicate) Create(e event.CreateEvent) bool {
	return p.ownerInNamespace(e.Meta.GetOwnerReferences())
}

// Update returns true if the event object's owner is in the same namespace
func (p OwnerInNamespacePredicate) Update(e event.UpdateEvent) bool {
	return p.ownerInNamespace(e.MetaNew.GetOwnerReferences())
}

// Delete returns true if the event object's owner is in the same namespace
func (p OwnerInNamespacePredicate) Delete(e event.DeleteEvent) bool {
	return p.ownerInNamespace(e.Meta.GetOwnerReferences())
}

// Generic returns true if the event object's owner is in the same namespace
func (p OwnerInNamespacePredicate) Generic(e event.GenericEvent) bool {
	return p.ownerInNamespace(e.Meta.GetOwnerReferences())
}

// ownerInNamespace returns true if the the GitTrack owner is in the namespace
// managed by the controller
//
// This works on the premise that listing objects from the client will only
// return those in its cache.
// When it is restricted to a namespace this should only be the GitTracks
// in the namespace the controller is managing.
func (p OwnerInNamespacePredicate) ownerInNamespace(ownerRefs []metav1.OwnerReference) bool {
	gtList := &farosv1alpha1.GitTrackList{}
	err := p.client.List(context.TODO(), &client.ListOptions{}, gtList)
	if err != nil {
		// We can't list CGTOs so fail closed and ignore the requests
		return false
	}
	for _, ref := range ownerRefs {
		if ref.Kind == "GitTrack" && ref.APIVersion == farosGroupVersion {
			for _, gt := range gtList.Items {
				if ref.UID == gt.UID {
					return true
				}
			}
		}
	}
	return false
}

// NewOwnerInNamespacePredicate constructs a new OwnerInNamespacePredicate
func NewOwnerInNamespacePredicate(client client.Client) OwnerInNamespacePredicate {
	return OwnerInNamespacePredicate{
		client: client,
	}
}

// OwnersOwnerInNamespacePredicate filters events to check the owners owner of
// the event object is in the controller's namespace
type OwnersOwnerInNamespacePredicate struct {
	client                    client.Client
	ownerInNamespacePredicate OwnerInNamespacePredicate
}

// Create returns true if the event object owners owner is in the same namespace
func (p OwnersOwnerInNamespacePredicate) Create(e event.CreateEvent) bool {
	return p.ownersOwnerInNamespace(e.Meta.GetOwnerReferences())
}

// Update returns true if the event object owners owner is in the same namespace
func (p OwnersOwnerInNamespacePredicate) Update(e event.UpdateEvent) bool {
	return p.ownersOwnerInNamespace(e.MetaNew.GetOwnerReferences())
}

// Delete returns true if the event object owners owner is in the same namespace
func (p OwnersOwnerInNamespacePredicate) Delete(e event.DeleteEvent) bool {
	return p.ownersOwnerInNamespace(e.Meta.GetOwnerReferences())
}

// Generic returns true if the event object owners owner is in the same namespace
func (p OwnersOwnerInNamespacePredicate) Generic(e event.GenericEvent) bool {
	return p.ownersOwnerInNamespace(e.Meta.GetOwnerReferences())
}

// ownersOwnerInNamespace returns true if the the GitTrackObject's GitTrack
// owner of the event object is in the namespace  managed by the controller
//
// This works on the premise that listing objects from the client will only
// return those in its cache.
// When it is restricted to a namespace this should only be the GitTracks
// in the namespace the controller is managing.
func (p OwnersOwnerInNamespacePredicate) ownersOwnerInNamespace(ownerRefs []metav1.OwnerReference) bool {
	cgtoList := &farosv1alpha1.ClusterGitTrackObjectList{}
	err := p.client.List(context.TODO(), &client.ListOptions{}, cgtoList)
	if err != nil {
		// We can't list CGTOs so fail closed and ignore the requests
		return false
	}
	gtoList := &farosv1alpha1.GitTrackObjectList{}
	err = p.client.List(context.TODO(), &client.ListOptions{}, gtoList)
	if err != nil {
		// We can't list GTOs so fail closed and ignore the requests
		return false
	}

	for _, ref := range ownerRefs {
		if ref.Kind == "GitTrackObject" && ref.APIVersion == farosGroupVersion {
			for _, gto := range gtoList.Items {
				if ref.UID == gto.UID {
					return p.ownerInNamespacePredicate.ownerInNamespace(gto.GetOwnerReferences())
				}
			}
		}
		if ref.Kind == "ClusterGitTrackObject" && ref.APIVersion == farosGroupVersion {
			for _, cgto := range cgtoList.Items {
				if ref.UID == cgto.UID {
					return p.ownerInNamespacePredicate.ownerInNamespace(cgto.GetOwnerReferences())
				}
			}
		}
	}
	return false
}

// NewOwnersOwnerInNamespacePredicate constructs a new OwnersOwnerInNamespacePredicate
func NewOwnersOwnerInNamespacePredicate(client client.Client) OwnersOwnerInNamespacePredicate {
	return OwnersOwnerInNamespacePredicate{
		client:                    client,
		ownerInNamespacePredicate: NewOwnerInNamespacePredicate(client),
	}
}
