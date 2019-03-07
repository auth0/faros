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

package gittrackobject

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"log"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	farosv1alpha1 "github.com/pusher/faros/pkg/apis/faros/v1alpha1"
	"github.com/pusher/faros/pkg/controller/gittrackobject/metrics"
	gittrackobjectutils "github.com/pusher/faros/pkg/controller/gittrackobject/utils"
	farosflags "github.com/pusher/faros/pkg/flags"
	"github.com/pusher/faros/pkg/utils"
	farosclient "github.com/pusher/faros/pkg/utils/client"
	testevents "github.com/pusher/faros/test/events"
	testutils "github.com/pusher/faros/test/utils"
	"golang.org/x/net/context"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var exampleDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
  namespace: default
  labels:
    app: nginx
spec:
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
     labels:
       app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx
`

var exampleDeployment2 = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
  namespace: default
  labels:
    app: nginx
spec:
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:latest
`

var exampleClusterRoleBinding = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: example
  labels:
    app: nginx
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nginx-ingress-controller
subjects:
- kind: ServiceAccount
  name: nginx-ingress-controller
  namespace: example
`

var exampleClusterRoleBinding2 = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: example
  labels:
    some.controller/enable-some-feature: "true"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nginx-ingress-controller
subjects:
- kind: ServiceAccount
  name: nginx-ingress-controller
  namespace: test
`

var invalidExample = `apiVersion: apps/v1
\x8f
kind: Deployment
metadata:
name: example
namespace: default
labels:
app: nginx
spec:
selector:
matchLabels:
app: nginx
template:
metadata:
labels:
app: nginx
spec:
containers:
- name: nginx
image: nginx:latest
`

var annotationExample = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
  namespace: default
  labels:
    app: nginx
  annotations:
    faros.pusher.com/update-strategy: "{{ .UpdateStrategy }}"
spec:
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:{{ .Tag }}
`

var deleteAnnotationExample = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
  namespace: default
  labels:
    app: nginx
  annotations:
    faros.pusher.com/resource-state: "{{ .ResourceState }}"
spec:
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:{{ .Tag }}
`

var clusterAnnotationExample = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: example
  labels:
    app: nginx
  annotations:
    faros.pusher.com/update-strategy: "{{ .UpdateStrategy }}"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ .Name }}
subjects:
- kind: ServiceAccount
  name: nginx-ingress-controller
  namespace: {{ .Namespace }}
`

var renderExample = func(ex string, values map[string]string) []byte {
	tmpl, err := template.New("object").Parse(ex)
	if err != nil {
		log.Fatalf("failed to parse template %v", err)
	}
	buff := new(bytes.Buffer)
	err = tmpl.Execute(buff, values)
	if err != nil {
		log.Fatalf("failed to execute template: %v", err)
	}
	return buff.Bytes()
}

var c client.Client

var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}}
var expectedClusterRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "example"}}
var depKey = types.NamespacedName{Name: "example", Namespace: "default"}
var crbKey = types.NamespacedName{Name: "example"}
var mgr manager.Manager
var instance *farosv1alpha1.GitTrackObject
var clusterInstance *farosv1alpha1.ClusterGitTrackObject
var gitTrack *farosv1alpha1.GitTrack
var requests chan reconcile.Request
var testEvents chan TestEvent
var stop chan struct{}
var stopInformers chan struct{}

const timeout = time.Second * 5
const consistentlyTimeout = time.Second

var _ = Describe("GitTrackObject Suite", func() {
	BeforeEach(func() {
		// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
		// channel when it is finished.
		var err error
		cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
		mgr, err = manager.New(cfg, manager.Options{
			Namespace:          farosflags.Namespace,
			MetricsBindAddress: "0", // Disable serving metrics while testing
		})
		Expect(err).NotTo(HaveOccurred())
		c = mgr.GetClient()

		var recFn reconcile.Reconciler
		testReconciler := newReconciler(mgr)
		recFn, testEvents = SetupTestEventRecorder(testReconciler)
		recFn, requests = SetupTestReconcile(recFn)
		Expect(add(mgr, recFn)).NotTo(HaveOccurred())
		stopInformers = testReconciler.(Reconciler).StopChan()
		stop = StartTestManager(mgr)

		gitTrack = &farosv1alpha1.GitTrack{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "testgittrack",
				Namespace: "default",
			},
			Spec: farosv1alpha1.GitTrackSpec{
				Reference:  "foo",
				Repository: "bar",
			},
		}
		Expect(c.Create(context.TODO(), gitTrack)).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		// Stop Controller and informers before cleaning up
		close(stop)
		close(stopInformers)
		// Clean up all resources as GC is disabled in the control plane
		testutils.DeleteAll(cfg, timeout,
			&farosv1alpha1.GitTrackList{},
			&farosv1alpha1.GitTrackObjectList{},
			&farosv1alpha1.ClusterGitTrackObjectList{},
			&appsv1.DeploymentList{},
			&rbacv1.ClusterRoleBindingList{},
			&v1.EventList{},
		)
	})

	Context("When a GitTrackObject is created", func() {
		Context("with YAML", func() {
			validDataTest([]byte(exampleDeployment), []byte(exampleDeployment2))
		})

		Context("with JSON", func() {

			// Convert the example deployment YAMLs to JSON
			example, _ := utils.YAMLToUnstructured([]byte(exampleDeployment))
			exampleDeploymentJSON, _ := example.MarshalJSON()
			if exampleDeploymentJSON == nil {
				panic("example JSON should not be empty!")
			}
			example2, _ := utils.YAMLToUnstructured([]byte(exampleDeployment2))
			exampleDeploymentJSON2, _ := example2.MarshalJSON()
			if exampleDeploymentJSON2 == nil {
				panic("example JSON 2 should not be empty!")
			}

			validDataTest(exampleDeploymentJSON, exampleDeploymentJSON2)
		})

		Context("with invalid data", func() {
			invalidDataTest()
		})

		Context("in a different namespace", func() {
			differentNamespaceTest()
		})
	})

	Context("When a ClusterGitTrackObject is created", func() {
		Context("with YAML", func() {
			validClusterDataTest([]byte(exampleClusterRoleBinding), []byte(exampleClusterRoleBinding2))
		})

		Context("with JSON", func() {

			// Convert the example deployment YAMLs to JSON
			example, _ := utils.YAMLToUnstructured([]byte(exampleClusterRoleBinding))
			exampleClusterRoleBindingJSON, _ := example.MarshalJSON()
			if exampleClusterRoleBindingJSON == nil {
				panic("example JSON should not be empty!")
			}
			example2, _ := utils.YAMLToUnstructured([]byte(exampleClusterRoleBinding2))
			exampleClusterRoleBindingJSON2, _ := example2.MarshalJSON()
			if exampleClusterRoleBindingJSON2 == nil {
				panic("example JSON 2 should not be empty!")
			}

			validClusterDataTest(exampleClusterRoleBindingJSON, exampleClusterRoleBindingJSON2)
		})

		Context("with invalid data", func() {
			invalidClusterDataTest()
		})

		Context("with its owner in a different namespace", func() {
			differentNamespaceOwnerTest()
		})
	})

	Context("When a GitTrackObject has an `update-strategy` annotation", func() {
		BeforeEach(func() {
			values := map[string]string{"UpdateStrategy": "update", "Tag": "v1.0.0"}
			CreateInstance(renderExample(annotationExample, values))
			// wait for create reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// wait for reconcile of status
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// Wait for client cache to expire
			WaitForStatus(depKey)
		})

		Context("and the value is `update`", func() {
			It("does update the resource", UpdateStrategyShouldUpdate)
		})

		Context("and the value is `recreate`", func() {
			It("patches resources with no conflicts", UpdateStrategyRecreateShouldPatch)
		})

		Context("and the value is `never`", func() {
			It("does not update the resource", UpdateStrategyShouldNever)
		})

		Context("and the value is anything else", func() {
			It("sets an error condition", UpdateStrategyError)
		})
	})

	Context("When a ClusterGitTrackObject has an `update-strategy` annotation", func() {
		BeforeEach(func() {
			values := map[string]string{"UpdateStrategy": "update", "Namespace": "default", "Name": "nginx-ingress-controller"}
			CreateClusterInstance(renderExample(clusterAnnotationExample, values))
			// wait for create reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			// wait for reconcile of status
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			// Wait for client cache to expire
			WaitForStatus(crbKey)
		})

		Context("and the value is `update`", func() {
			It("does update the resource", ClusterUpdateStrategyShouldUpdate)
		})

		Context("and the value is `recreate`", func() {
			It("patches resources without conflicts", ClusterUpdateStrategyRecreateShouldPatch)
			It("recreates resources with conflicts", ClusterUpdateStrategyRecreateShouldRecreate)
		})

		Context("and the value is `never`", func() {
			It("does not update the resource", ClusterUpdateStrategyShouldNever)
		})

		Context("and the value is anything else", func() {
			It("sets an error condition", ClusterUpdateStrategyError)
		})
	})

	Context("When a GitTrackObject has a `resource-state` annotation", func() {
		BeforeEach(func() {
			CreateInstance([]byte(exampleDeployment2))
			// wait for create reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// wait for reconcile of status
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// Wait for client cache to expire
			WaitForStatus(depKey)
		})

		Context("and the value is `active`", func() {
			It("does update the resource", ResourceStateShouldActive)
		})

		Context("and the value is `marked-for-deletion`", func() {
			It("does delete the resource", ResourceStateShouldMarkedForDeletion)
		})

		Context("and the value is anything else", func() {
			It("sets an error condition", ResourceStateError)
		})
	})
})

var (
	// CreateInstance creates the instance and waits for a reconcile to happen.
	CreateInstance = func(data []byte) {
		instance = &farosv1alpha1.GitTrackObject{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "example",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "faros.pusher.com/v1alpha1",
						Kind:       "GitTrack",
						UID:        gitTrack.UID,
						Name:       gitTrack.Name,
					},
				},
			},
			Spec: farosv1alpha1.GitTrackObjectSpec{
				Name: "deployment-example",
				Kind: "Deployment",
				Data: data,
			},
		}
		// Create the GitTrackObject object and expect the Reconcile to occur
		err := c.Create(context.TODO(), instance)
		Expect(err).NotTo(HaveOccurred())
	}

	// CreateClusterInstance creates the clusterInstance and waits for a reconcile to happen.
	CreateClusterInstance = func(data []byte) {
		clusterInstance = &farosv1alpha1.ClusterGitTrackObject{
			ObjectMeta: metav1.ObjectMeta{
				Name: "example",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "faros.pusher.com/v1alpha1",
						Kind:       "GitTrack",
						UID:        gitTrack.UID,
						Name:       gitTrack.Name,
					},
				},
			},
			Spec: farosv1alpha1.GitTrackObjectSpec{
				Name: "clusterrolebinding-example",
				Kind: "ClusterRoleBinding",
				Data: data,
			},
		}
		// Create the ClusterGitTrackObject object and expect the Reconcile to occur
		err := c.Create(context.TODO(), clusterInstance)
		Expect(err).NotTo(HaveOccurred())
	}

	UpdateInstance = func(i *farosv1alpha1.GitTrackObject, data []byte, statusUpdate bool) {
		i.Spec.Data = data
		Expect(c.Update(context.TODO(), i)).ShouldNot(HaveOccurred())
		// Wait for update reconcile to happen
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		if statusUpdate {
			// Wait for status reconcile to happen
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		}
	}

	UpdateClusterInstance = func(i *farosv1alpha1.ClusterGitTrackObject, data []byte, statusUpdate bool) {
		i.Spec.Data = data
		Expect(c.Update(context.TODO(), i)).ShouldNot(HaveOccurred())
		// Wait for update reconcile to happen
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		if statusUpdate {
			// Wait for status reconcile to happen
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		}
	}

	// DeleteClusterInstance deletes the clusterInstance
	DeleteClusterInstance = func() {
		err := c.Delete(context.TODO(), clusterInstance)
		Expect(err).NotTo(HaveOccurred())
	}

	CheckImageTag = func(imageTag string) error {
		deploy := &appsv1.Deployment{}
		err := c.Get(context.TODO(), depKey, deploy)
		if err != nil {
			return err
		}
		if deploy.Spec.Template.Spec.Containers[0].Image != imageTag {
			return fmt.Errorf("image hasn't updated")
		}
		return nil
	}

	WaitForStatus = func(key types.NamespacedName) {
		var obj farosv1alpha1.GitTrackObjectInterface
		if key.Namespace != "" {
			obj = &farosv1alpha1.GitTrackObject{}
		} else {
			obj = &farosv1alpha1.ClusterGitTrackObject{}
		}
		Eventually(func() error {
			err := c.Get(context.TODO(), key, obj)
			if err != nil {
				return err
			}
			if len(obj.GetStatus().Conditions) == 0 {
				return fmt.Errorf("Status not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	// validDataTest runs the suite of tests for valid input data
	validDataTest = func(initial, updated []byte) {
		BeforeEach(func() {
			CreateInstance(initial)
			// wait for first reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// wait for reconcile of status
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// Wait for client cache to expire
			WaitForStatus(depKey)
		})

		It("should create it's child resource", ShouldCreateChild)

		It("should add an owner reference to the child", ShouldAddOwnerReference)

		It("should add an last applied annotation to the child", ShouldAddLastApplied)

		Context("should update the status", func() {
			It("condition status should be true", func() {
				ShouldUpdateConditionStatus(v1.ConditionTrue)
			})
			It("condition reason should be ChildAppliedSuccess", func() {
				ShouldUpdateConditionReason(gittrackobjectutils.ChildAppliedSuccess, true)
			})
		})

		Context("should update the metrics", func() {
			It("should set in-sync metric to 1", func() {
				ShouldSetInSyncMetricTo(instance, 1.0)
			})
		})

		It("should update the resource when the GTO is updated", func() {
			ShouldUpdateChildOnGTOUpdate(updated)
		})

		It("should no update the resource if the GTO's metadata is updated", ShouldNotUpdateChildOnGTOUpdate)

		It("should recreate the child if it is deleted", ShouldRecreateChildIfDeleted)

		Context("should reset the child if", func() {
			It("the spec is modified", ShouldResetChildIfSpecModified)

			It("the meta is modified", ShouldResetChildIfMetaModified)
		})

		It("should send `CreateStarted` and `CreateSuccessful` events", func() {
			ShouldSendCreateEvents("GitTrackObject", "default")
		})

		It("should send all events to the namespace the controller is restricted to", ShouldSendAllEventsToNamespace)
	}

	// validDataTest runs the suite of tests for valid input data
	validClusterDataTest = func(initial, updated []byte) {
		BeforeEach(func() {
			CreateClusterInstance(initial)
			// Wait for create reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			// Wait for status reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			// Wait for client cache to expire
			WaitForStatus(crbKey)
		})

		It("should create it's child resource", ClusterShouldCreateChild)

		It("should add an owner reference to the child", ClusterShouldAddOwnerReference)

		It("should add an last applied annotation to the child", ClusterShouldAddLastApplied)

		Context("should update the status", func() {
			It("condition status should be true", func() {
				ClusterShouldUpdateConditionStatus(v1.ConditionTrue)
			})
			It("condition reason should be ChildAppliedSuccess", func() {
				ClusterShouldUpdateConditionReason(gittrackobjectutils.ChildAppliedSuccess, true)
			})
		})

		Context("should update the metrics", func() {
			It("should set in-sync metric to 1", func() {
				ShouldSetInSyncMetricTo(clusterInstance, 1.0)
			})
		})

		It("should update the resource when the GTO is updated", func() {
			ClusterShouldUpdateChildOnGTOUpdate(updated)
		})

		It("should no update the resource if the GTO's metadata is updated", ClusterShouldNotUpdateChildOnGTOUpdate)

		It("should recreate the child if it is deleted", ClusterShouldRecreateChildIfDeleted)

		Context("should reset the child if", func() {
			It("the spec is modified", ClusterShouldResetChildIfSpecModified)

			It("the meta is modified", ClusterShouldResetChildIfMetaModified)
		})

		It("should send `CreateStarted` and `CreateSuccessful` events", func() {
			ShouldSendCreateEvents("ClusterGitTrackObject", "default")
		})

		It("should send all events to the namespace the controller is restricted to", ShouldSendAllEventsToNamespace)
	}

	// invalidDataTest runs the suite of tests for an invalid input
	invalidDataTest = func() {
		BeforeEach(func() {
			CreateInstance([]byte(invalidExample))
			// wait for first reconcile
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// wait for reconcile of status
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
			// Wait for client cache to expire
			WaitForStatus(depKey)
		})

		Context("should update the status", func() {
			It("condition status should be false", func() {
				ShouldUpdateConditionStatus(v1.ConditionFalse)
			})

			It("condition reason should not be ChildAppliedSuccess", func() {
				ShouldUpdateConditionReason(gittrackobjectutils.ChildAppliedSuccess, false)
			})
		})

		It("should send `UnmarshalFailed` event", func() {
			ShouldSendFailedUnmarshalEvent("GitTrackObject", "default")
		})
	}

	// invalidClusterDataTest runs the suite of tests for an invalid input
	invalidClusterDataTest = func() {
		BeforeEach(func() {
			CreateClusterInstance([]byte(invalidExample))
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			// Wait for client cache to expire
			WaitForStatus(crbKey)
		})

		Context("should update the status", func() {
			It("condition status should be false", func() {
				ClusterShouldUpdateConditionStatus(v1.ConditionFalse)
			})
			It("condition reason should not be ChildAppliedSuccess", func() {
				ClusterShouldUpdateConditionReason(gittrackobjectutils.ChildAppliedSuccess, false)
			})
		})

		It("should send a `UnmarshalFailed` event", func() {
			ShouldSendFailedUnmarshalEvent("ClusterGitTrackObject", "default")
		})
	}

	differentNamespaceOwnerTest = func() {
		var ns *v1.Namespace
		var gt *farosv1alpha1.GitTrack
		BeforeEach(func() {
			CreateClusterInstance([]byte(exampleClusterRoleBinding))
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
			Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))

			ns = &v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "not-default",
				},
			}
			c.Create(context.TODO(), ns)

			gt = &farosv1alpha1.GitTrack{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testgittrack",
					Namespace: "cluster-not-default",
				},
				Spec: farosv1alpha1.GitTrackSpec{
					Reference:  "foo",
					Repository: "bar",
				},
			}
			Expect(c.Create(context.TODO(), gt)).NotTo(HaveOccurred())

			key := types.NamespacedName{Name: "example"}
			Expect(c.Get(context.TODO(), key, clusterInstance)).NotTo(HaveOccurred())
			clusterInstance.SetOwnerReferences([]metav1.OwnerReference{
				{
					APIVersion: "faros.pusher.com/v1alpha1",
					Kind:       "GitTrack",
					Name:       gt.Name,
					UID:        gt.UID,
				},
			})
			Expect(c.Update(context.TODO(), clusterInstance)).NotTo(HaveOccurred())
		})

		It("should not reconcile it", func() {
			Consistently(requests).ShouldNot(Receive())
		})

		It("should not reconcile when the child is modified", func() {
			crb := &rbacv1.ClusterRoleBinding{}
			key := types.NamespacedName{Name: "example"}
			Expect(c.Get(context.TODO(), key, crb)).NotTo(HaveOccurred())

			crb.SetLabels(map[string]string{})
			Expect(c.Update(context.TODO(), crb)).NotTo(HaveOccurred())

			Consistently(requests).ShouldNot(Receive())
		})
	}

	differentNamespaceTest = func() {
		var ns *v1.Namespace
		BeforeEach(func() {
			ns = &v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "not-default",
				},
			}
			c.Create(context.TODO(), ns)

			instance = &farosv1alpha1.GitTrackObject{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "example",
					Namespace: "not-default",
				},
				Spec: farosv1alpha1.GitTrackObjectSpec{
					Name: "deployment-example",
					Kind: "Deployment",
					Data: []byte(exampleDeployment),
				},
			}
			Expect(c.Create(context.TODO(), instance)).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			Expect(c.Delete(context.TODO(), instance)).NotTo(HaveOccurred())
			key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name}
			Eventually(c.Get(context.TODO(), key, instance), timeout).ShouldNot(Succeed())
		})

		It("should not reconcile it", func() {
			Consistently(requests, consistentlyTimeout).ShouldNot(Receive())
		})

	}

	// ShouldCreateChild checks the child object was created
	ShouldCreateChild = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())
	}

	// ClusterShouldCreateChild checks the child object was created
	ClusterShouldCreateChild = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())
	}

	// ShouldAddOwnerReference checks the owner reference was set
	ShouldAddOwnerReference = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		Expect(len(deploy.OwnerReferences)).To(Equal(1))
		oRef := deploy.OwnerReferences[0]
		Expect(oRef.APIVersion).To(Equal("faros.pusher.com/v1alpha1"))
		Expect(oRef.Kind).To(Equal("GitTrackObject"))
		Expect(oRef.Name).To(Equal(instance.Name))
	}

	// ClusterShouldAddOwnerReference checks the owner reference was set
	ClusterShouldAddOwnerReference = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		Expect(len(crb.OwnerReferences)).To(Equal(1))
		oRef := crb.OwnerReferences[0]
		Expect(oRef.APIVersion).To(Equal("faros.pusher.com/v1alpha1"))
		Expect(oRef.Kind).To(Equal("ClusterGitTrackObject"))
		Expect(oRef.Name).To(Equal(clusterInstance.Name))
	}

	// ShouldAddLastApplied checks that the last applied annotation was set
	ShouldAddLastApplied = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		annotations := deploy.ObjectMeta.Annotations
		_, ok := annotations[farosclient.LastAppliedAnnotation]
		Expect(ok).To(BeTrue())
	}

	// ClusterShouldAddLastApplied checks that the last applied annotation was set
	ClusterShouldAddLastApplied = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		annotations := crb.ObjectMeta.Annotations
		_, ok := annotations[farosclient.LastAppliedAnnotation]
		Expect(ok).To(BeTrue())
	}

	// ShouldUpdateConditionStatus checks the condition status was set
	ShouldUpdateConditionStatus = func(expected v1.ConditionStatus) {
		if expected == v1.ConditionTrue {
			deploy := &appsv1.Deployment{}
			Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
				Should(Succeed())
		}

		err := c.Get(context.TODO(), depKey, instance)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(len(instance.Status.Conditions)).To(Equal(1))
		condition := instance.Status.Conditions[0]
		Expect(condition.Type).To(Equal(farosv1alpha1.ObjectInSyncType))
		Expect(condition.Status).To(Equal(expected))
	}

	// ClusterShouldUpdateConditionStatus checks the condition status was set
	ClusterShouldUpdateConditionStatus = func(expected v1.ConditionStatus) {
		if expected == v1.ConditionTrue {
			crb := &rbacv1.ClusterRoleBinding{}
			Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
				Should(Succeed())
		}

		err := c.Get(context.TODO(), crbKey, clusterInstance)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(len(clusterInstance.Status.Conditions)).To(Equal(1))
		condition := clusterInstance.Status.Conditions[0]
		Expect(condition.Type).To(Equal(farosv1alpha1.ObjectInSyncType))
		Expect(condition.Status).To(Equal(expected))
	}

	// ShouldUpdateConditionReason checks the condition reason was set
	ShouldUpdateConditionReason = func(expected gittrackobjectutils.ConditionReason, match bool) {
		err := c.Get(context.TODO(), depKey, instance)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(len(instance.Status.Conditions)).To(Equal(1))
		condition := instance.Status.Conditions[0]
		matcher := Equal(string(expected))
		if !match {
			matcher = Not(matcher)
		}
		Expect(condition.Reason).To(matcher)

		if condition.Status == v1.ConditionTrue {
			deploy := &appsv1.Deployment{}
			Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
				Should(Succeed())
		}
	}

	// ClusterShouldUpdateConditionReason checks the condition reason was set
	ClusterShouldUpdateConditionReason = func(expected gittrackobjectutils.ConditionReason, match bool) {
		err := c.Get(context.TODO(), crbKey, clusterInstance)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(len(clusterInstance.Status.Conditions)).To(Equal(1))
		condition := clusterInstance.Status.Conditions[0]
		matcher := Equal(string(expected))
		if !match {
			matcher = Not(matcher)
		}
		Expect(condition.Reason).To(matcher)

		if condition.Status == v1.ConditionTrue {
			crb := &rbacv1.ClusterRoleBinding{}
			Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
				Should(Succeed())
		}
	}

	// ShouldUpdateChildOnGTOUpdate updates the GitTrackObject and checks the
	// update propogates to the child
	ShouldUpdateChildOnGTOUpdate = func(data []byte) {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		// Fetch the instance and update it
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).
			Should(Succeed())
		instance.Spec.Data = data
		err := c.Update(context.TODO(), instance)
		Expect(err).ShouldNot(HaveOccurred())

		// Wait for a reconcile to happen
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))

		Eventually(func() error {
			err := c.Get(context.TODO(), depKey, deploy)
			if err != nil {
				return err
			}
			if len(deploy.Spec.Template.Spec.Containers) != 1 ||
				deploy.Spec.Template.Spec.Containers[0].Image != "nginx:latest" {
				return errors.New("Image not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	// ClusterShouldUpdateChildOnGTOUpdate updates the GitTrackObject and checks the
	// update propogates to the child
	ClusterShouldUpdateChildOnGTOUpdate = func(data []byte) {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		// Fetch the instance and update it
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).
			Should(Succeed())
		clusterInstance.Spec.Data = data
		err := c.Update(context.TODO(), clusterInstance)
		Expect(err).ShouldNot(HaveOccurred())

		// Wait for a reconcile to happen
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))

		Eventually(func() error {
			err := c.Get(context.TODO(), crbKey, crb)
			if err != nil {
				return err
			}
			if len(crb.ObjectMeta.Labels) != 1 ||
				crb.ObjectMeta.Labels["some.controller/enable-some-feature"] != "true" {
				return errors.New("Lables not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	// ShouldNotUpdateChildOnGTOUpdate updates the GitTrackObject and checks the
	// child which should not have been updated
	ShouldNotUpdateChildOnGTOUpdate = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		originalVersion := deploy.ObjectMeta.ResourceVersion

		// Fetch the instance and update it
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).
			Should(Succeed())
		if instance.ObjectMeta.Labels == nil {
			instance.ObjectMeta.Labels = make(map[string]string)
		}
		instance.ObjectMeta.Labels["newLabel"] = "newLabel"
		err := c.Update(context.TODO(), instance)
		Expect(err).ShouldNot(HaveOccurred())

		// Wait for a reconcile to happen
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))

		err = c.Get(context.TODO(), depKey, deploy)
		Expect(err).ToNot(HaveOccurred())
		Expect(deploy.ObjectMeta.ResourceVersion).To(Equal(originalVersion))
	}

	// ClusterShouldNotUpdateChildOnGTOUpdate updates the GitTrackObject and checks the
	// child which should not have been updated
	ClusterShouldNotUpdateChildOnGTOUpdate = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		originalVersion := crb.ObjectMeta.ResourceVersion

		// Fetch the instance and update it
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).
			Should(Succeed())
		if clusterInstance.ObjectMeta.Labels == nil {
			clusterInstance.ObjectMeta.Labels = make(map[string]string)
		}
		clusterInstance.ObjectMeta.Labels["newLabel"] = "newLabel"
		err := c.Update(context.TODO(), clusterInstance)
		Expect(err).ShouldNot(HaveOccurred())

		// Wait for a reconcile to happen
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))

		err = c.Get(context.TODO(), crbKey, crb)
		Expect(err).ToNot(HaveOccurred())
		Expect(crb.ObjectMeta.ResourceVersion).To(Equal(originalVersion))
	}

	// ShouldRecreateChildIfDeleted deletes the child and expects it to be
	// recreated
	ShouldRecreateChildIfDeleted = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		// Delete the instance and expect it to be recreated
		Expect(c.Delete(context.TODO(), deploy)).To(Succeed())
		// wait for reconcile of delete
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		// wait for reconcile of status
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())
	}

	// ClusterShouldRecreateChildIfDeleted deletes the child and expects it to be
	// recreated
	ClusterShouldRecreateChildIfDeleted = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		// Delete the instance and expect it to be recreated
		Expect(c.Delete(context.TODO(), crb)).To(Succeed())
		// wait for reconcile of delete
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		// wait for reconcile of status
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())
	}

	// ShouldResetChildIfSpecModified modifies the child spec and checks that
	// it is reset by the controller
	ShouldResetChildIfSpecModified = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		// Update the spec and expect it to be reset
		deploy.Spec.Template.Spec.Containers[0].Image = "nginx:latest"
		Expect(c.Update(context.TODO(), deploy)).To(Succeed())
		// Wait for reconcile for update
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		// Wait for reconcile for status
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		Eventually(func() error {
			err := c.Get(context.TODO(), depKey, deploy)
			if err != nil {
				return err
			}
			if len(deploy.Spec.Template.Spec.Containers) != 1 ||
				deploy.Spec.Template.Spec.Containers[0].Image != "nginx" {
				return errors.New("Image not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	// ClusterShouldResetChildIfSpecModified modifies the child spec and checks that
	// it is reset by the controller
	ClusterShouldResetChildIfSpecModified = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		// Update the spec and expect it to be reset
		crb.Subjects[0].Namespace = "test"
		Expect(c.Update(context.TODO(), crb)).To(Succeed())
		// wait for reconcile of delete
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		// wait for reconcile of status
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		Eventually(func() error {
			err := c.Get(context.TODO(), crbKey, crb)
			if err != nil {
				return err
			}
			if len(crb.Subjects) != 1 ||
				crb.Subjects[0].Namespace != "example" {
				return errors.New("Subject not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	// ShouldResetChildIfMetaModified modifies the child metadata and checks that
	// it is reset by the controller
	ShouldResetChildIfMetaModified = func() {
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
			Should(Succeed())

		// Update the spec and expect it to be reset
		deploy.ObjectMeta.Labels = map[string]string{"app": "nginx-ingress"}
		Expect(c.Update(context.TODO(), deploy)).To(Succeed())
		// Wait for update reconcile
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		// Wait for status reconcile
		Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
		Eventually(func() error {
			err := c.Get(context.TODO(), depKey, deploy)
			if err != nil {
				return err
			}

			if len(deploy.ObjectMeta.Labels) != 1 ||
				deploy.ObjectMeta.Labels["app"] != "nginx" {
				return errors.New("Labels not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	// ClusterShouldResetChildIfSpecModified modifies the child spec and checks that
	// it is reset by the controller
	ClusterShouldResetChildIfMetaModified = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).
			Should(Succeed())

		// Update the spec and expect it to be reset
		crb.ObjectMeta.Labels = map[string]string{"app": "nginx-ingress"}
		Expect(c.Update(context.TODO(), crb)).To(Succeed())
		Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
		Eventually(func() error {
			err := c.Get(context.TODO(), crbKey, crb)
			if err != nil {
				return err
			}

			if len(crb.ObjectMeta.Labels) != 1 ||
				crb.ObjectMeta.Labels["app"] != "nginx" {
				return errors.New("Labels not updated")
			}
			return nil
		}, timeout).Should(Succeed())
	}

	UpdateStrategyShouldUpdate = func() {
		values := map[string]string{"UpdateStrategy": "update", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v1.0.0"))
		UpdateInstance(instance, renderExample(annotationExample, values), true)
		Eventually(func() error { return CheckImageTag("nginx:v2.0.0") }, timeout).Should(Succeed())
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		container = deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v2.0.0"))
	}

	UpdateStrategyRecreateShouldPatch = func() {
		values := map[string]string{"UpdateStrategy": "recreate", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		beforeUID := deploy.ObjectMeta.UID
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v1.0.0"))
		UpdateInstance(instance, renderExample(annotationExample, values), true)
		Eventually(func() error { return CheckImageTag("nginx:v2.0.0") }, timeout).Should(Succeed())
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		afterUID := deploy.ObjectMeta.UID
		container = deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v2.0.0"))
		Expect(beforeUID).To(Equal(afterUID))
	}

	UpdateStrategyShouldNever = func() {
		values := map[string]string{"UpdateStrategy": "never", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v1.0.0"))
		UpdateInstance(instance, renderExample(annotationExample, values), false)
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		container = deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v1.0.0"))
	}

	UpdateStrategyError = func() {
		values := map[string]string{"UpdateStrategy": "anything-else", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		UpdateInstance(instance, renderExample(annotationExample, values), true)
		Eventually(func() error {
			err := c.Get(context.TODO(), depKey, instance)
			if err != nil {
				return err
			}
			if instance.Status.Conditions[0].Reason != string(gittrackobjectutils.ErrorUpdatingChild) {
				return fmt.Errorf("condition hasn't been updated")
			}
			return nil
		}, timeout).Should(Succeed())
		condition := instance.Status.Conditions[0]
		Expect(condition.Message).To(MatchRegexp("unable to get update strategy: invalid update strategy: anything-else"))
	}

	ResourceStateShouldActive = func() {
		values := map[string]string{"ResourceState": "active", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:latest"))
		UpdateInstance(instance, renderExample(deleteAnnotationExample, values), true)
		Eventually(func() error { return CheckImageTag("nginx:v2.0.0") }, timeout).Should(Succeed())
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		container = deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal("nginx:v2.0.0"))
	}

	ResourceStateShouldMarkedForDeletion = func() {
		values := map[string]string{"ResourceState": "marked-for-deletion", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		deploy := &appsv1.Deployment{}
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).Should(Succeed())
		UpdateInstance(instance, renderExample(deleteAnnotationExample, values), true)
		Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).ShouldNot(Succeed())
	}

	ResourceStateError = func() {
		values := map[string]string{"ResourceState": "anything-else", "Tag": "v2.0.0"}
		Eventually(func() error { return c.Get(context.TODO(), depKey, instance) }, timeout).Should(Succeed())
		UpdateInstance(instance, renderExample(deleteAnnotationExample, values), true)
		Eventually(func() error {
			err := c.Get(context.TODO(), depKey, instance)
			if err != nil {
				return err
			}
			if instance.Status.Conditions[0].Reason != string(gittrackobjectutils.ErrorUpdatingChild) {
				return fmt.Errorf("condition hasn't been updated")
			}
			return nil
		}, timeout).Should(Succeed())
		condition := instance.Status.Conditions[0]
		Expect(condition.Message).To(MatchRegexp("unable to get resource state: invalid resource state: anything-else"))
	}

	ClusterUpdateStrategyShouldUpdate = func() {
		values := map[string]string{"UpdateStrategy": "update", "Namespace": "other", "Name": "nginx-ingress-controller"}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).Should(Succeed())
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		Expect(crb.Subjects[0].Namespace).To(Equal("default"))
		UpdateClusterInstance(clusterInstance, renderExample(clusterAnnotationExample, values), true)
		Eventually(func() error {
			crb = &rbacv1.ClusterRoleBinding{}
			err := c.Get(context.TODO(), crbKey, crb)
			if err != nil {
				return err
			}
			if crb.Subjects[0].Namespace != "other" {
				return fmt.Errorf("namespace hasn't been updated")
			}
			return nil
		}, timeout).Should(Succeed())
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		Expect(crb.Subjects[0].Namespace).To(Equal("other"))
	}

	ClusterUpdateStrategyRecreateShouldPatch = func() {
		values := map[string]string{"UpdateStrategy": "recreate", "Namespace": "other", "Name": "nginx-ingress-controller"}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).Should(Succeed())
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		beforeUID := crb.ObjectMeta.UID
		Expect(crb.Subjects[0].Namespace).To(Equal("default"))
		UpdateClusterInstance(clusterInstance, renderExample(clusterAnnotationExample, values), true)
		Eventually(func() error {
			crb = &rbacv1.ClusterRoleBinding{}
			err := c.Get(context.TODO(), crbKey, crb)
			if err != nil {
				return err
			}
			if crb.Subjects[0].Namespace != "other" {
				return fmt.Errorf("namespace hasn't been updated")
			}
			return nil
		}, timeout).Should(Succeed())
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		afterUID := crb.ObjectMeta.UID
		Expect(crb.Subjects[0].Namespace).To(Equal("other"))
		Expect(beforeUID).To(Equal(afterUID))
	}

	ClusterUpdateStrategyRecreateShouldRecreate = func() {
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		beforeUID := crb.ObjectMeta.UID
		Expect(crb.RoleRef.Name).To(Equal("nginx-ingress-controller"))

		// Make sure to clear the foregroundDeletion finalizer
		go func() {
			Eventually(func() error {
				err := c.Get(context.TODO(), crbKey, crb)
				if err != nil {
					return err
				}
				if len(crb.Finalizers) == 0 {
					return fmt.Errorf("Not deleted yet")
				}
				crb.Finalizers = []string{}
				return c.Update(context.TODO(), crb)
			}, timeout).Should(Succeed())
		}()

		// Role reference is immutable so should cause a conflict, causing the clusterrolebinding to be re-createad
		values := map[string]string{"UpdateStrategy": "recreate", "Namespace": "default", "Name": "other"}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).Should(Succeed())
		UpdateClusterInstance(clusterInstance, renderExample(clusterAnnotationExample, values), true)
		Eventually(func() error {
			crb = &rbacv1.ClusterRoleBinding{}
			err := c.Get(context.TODO(), crbKey, crb)
			if err != nil {
				return err
			}
			if crb.RoleRef.Name != "other" {
				return fmt.Errorf("namespace hasn't been updated")
			}
			return nil
		}, timeout).Should(Succeed())
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		afterUID := crb.ObjectMeta.UID
		Expect(crb.RoleRef.Name).To(Equal("other"))
		Expect(beforeUID).ToNot(Equal(afterUID))
	}

	ClusterUpdateStrategyShouldNever = func() {
		values := map[string]string{"UpdateStrategy": "never", "Namespace": "other", "Name": "nginx-ingress-controller"}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).Should(Succeed())
		crb := &rbacv1.ClusterRoleBinding{}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		Expect(crb.Subjects[0].Namespace).To(Equal("default"))
		UpdateClusterInstance(clusterInstance, renderExample(clusterAnnotationExample, values), false)
		Eventually(func() error { return c.Get(context.TODO(), crbKey, crb) }, timeout).Should(Succeed())
		Expect(crb.Subjects[0].Namespace).To(Equal("default"))
	}

	ClusterUpdateStrategyError = func() {
		values := map[string]string{"UpdateStrategy": "anything-else", "Namespace": "other", "Name": "nginx-ingress-controller"}
		Eventually(func() error { return c.Get(context.TODO(), crbKey, clusterInstance) }, timeout).Should(Succeed())
		UpdateClusterInstance(clusterInstance, renderExample(clusterAnnotationExample, values), true)
		Eventually(func() error {
			err := c.Get(context.TODO(), crbKey, clusterInstance)
			if err != nil {
				return err
			}
			if clusterInstance.Status.Conditions[0].Reason != string(gittrackobjectutils.ErrorUpdatingChild) {
				return fmt.Errorf("condition hasn't been updated")
			}
			return nil
		}, timeout).Should(Succeed())
		condition := clusterInstance.Status.Conditions[0]
		Expect(condition.Reason).To(Equal(string(gittrackobjectutils.ErrorUpdatingChild)))
		Expect(condition.Message).To(MatchRegexp("unable to get update strategy: invalid update strategy: anything-else"))
	}

	ShouldSendFailedUnmarshalEvent = func(kind, namespace string) {
		events := &v1.EventList{}
		filter := func(ev v1.Event) bool {
			return ev.Reason == "UnmarshalFailed" && ev.InvolvedObject.Kind == kind
		}
		Eventually(func() error {
			err := c.List(context.TODO(), &client.ListOptions{}, events)
			if err != nil {
				return err
			}
			if testevents.None(events.Items, filter) {
				return fmt.Errorf("event haven't been sent yet")
			}
			return nil
		}, timeout).Should(Succeed())
		failedEvents := testevents.Select(events.Items, filter)
		Expect(failedEvents).ToNot(BeEmpty())
		for _, e := range failedEvents {
			Expect(e.InvolvedObject.Kind).To(Equal(kind))
			Expect(e.InvolvedObject.Name).To(Equal("example"))
			Expect(e.InvolvedObject.Namespace).To(Equal(namespace))
			Expect(e.Type).To(Equal(string(v1.EventTypeWarning)))
		}
	}

	ShouldSendCreateEvents = func(kind, namespace string) {
		events := &v1.EventList{}
		filter := func(r, k string) func(v1.Event) bool {
			return func(ev v1.Event) bool { return ev.Reason == r && ev.InvolvedObject.Kind == k }
		}
		Eventually(func() error {
			err := c.List(context.TODO(), &client.ListOptions{}, events)
			if err != nil {
				return err
			}
			if testevents.None(events.Items, filter("CreateSuccessful", kind)) {
				return fmt.Errorf("events haven't been sent yet")
			}
			return nil
		}, timeout).Should(Succeed())

		startEvents := testevents.Select(events.Items, filter("CreateStarted", kind))
		successEvents := testevents.Select(events.Items, filter("CreateSuccessful", kind))
		Expect(startEvents).ToNot(BeEmpty())
		Expect(successEvents).ToNot(BeEmpty())
		for _, e := range append(startEvents, successEvents...) {
			Expect(e.InvolvedObject.Kind).To(Equal(kind))
			Expect(e.InvolvedObject.Name).To(Equal("example"))
			Expect(e.InvolvedObject.Namespace).To(Equal(namespace))
			Expect(e.Type).To(Equal(string(v1.EventTypeNormal)))
		}
	}

	ShouldSetInSyncMetricTo = func(instance farosv1alpha1.GitTrackObjectInterface, value float64) {
		var gauge prometheus.Gauge
		Eventually(func() error {
			var err error
			gauge, err = metrics.InSync.GetMetricWith(map[string]string{
				"kind":      instance.GetSpec().Kind,
				"name":      instance.GetSpec().Name,
				"namespace": instance.GetNamespace(),
			})
			return err
		}, timeout).Should(Succeed())
		var metric dto.Metric
		Expect(gauge.Write(&metric)).NotTo(HaveOccurred())
		Expect(metric.GetGauge().GetValue()).To(Equal(value))
	}

	ShouldSendAllEventsToNamespace = func(done Done) {
		events := &v1.EventList{}
		Eventually(func() error {
			return c.List(context.TODO(), &client.ListOptions{}, events)
		}, timeout).Should(Succeed())

		for range events.Items {
			event := <-testEvents
			Expect(event.Namespace).To(Equal(farosflags.Namespace))
		}

		close(done)
	}
)
