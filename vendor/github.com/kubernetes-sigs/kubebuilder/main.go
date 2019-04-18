package main

import (
	"context"
	"flag"
	"os"

	"github.com/kubernetes-sigs/kubebuilder/pkg/client"
	"github.com/kubernetes-sigs/kubebuilder/pkg/config"
	"github.com/kubernetes-sigs/kubebuilder/pkg/ctrl"
	"github.com/kubernetes-sigs/kubebuilder/pkg/ctrl/eventhandler"
	"github.com/kubernetes-sigs/kubebuilder/pkg/ctrl/reconcile"
	"github.com/kubernetes-sigs/kubebuilder/pkg/ctrl/source"
	logf "github.com/kubernetes-sigs/kubebuilder/pkg/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var log = logf.Log.WithName("main")

func main() {
	// Init main
	flag.Parse()
	logf.SetLogger(logf.ZapLogger(true))

	// Init controller
	cm, err := ctrl.NewControllerManager(ctrl.ControllerManagerArgs{Config: config.GetConfigOrDie()})
	if err != nil {
		log.Error(err, "Could not get a rest.Config")
		os.Exit(1)
	}
	cm.GetFieldIndexer().IndexField(&corev1.Endpoints{}, "synthetic.targets", func(obj runtime.Object) []string {
		ep := obj.(*corev1.Endpoints)
		var res []string
		for _, subset := range ep.Subsets {
			for _, addr := range subset.Addresses {
				res = append(res, addr.TargetRef.Name)
			}
		}
		return res
	})

	c := cm.NewController(ctrl.ControllerArgs{Name: "sample-controller"},
		reconcile.ReconcileFunc(func(request reconcile.ReconcileRequest) (reconcile.ReconcileResult, error) {
			log.Info("Got reconcile", "Request", request)
			return reconcile.ReconcileResult{}, nil
		}))

	c.Watch(&source.KindSource{Type: &corev1.Endpoints{}}, &eventhandler.EnqueueHandler{})
	c.Watch(&source.KindSource{Type: &corev1.Pod{}}, &eventhandler.EnqueueMappedHandler{
		ToRequests: eventhandler.ToRequestsFunc(func(evt eventhandler.MapObject) []reconcile.ReconcileRequest {
			pods := &corev1.PodList{}
			cm.GetClient().List(context.TODO(), client.MatchingField("synthetic.targets", evt.Meta.GetName()), pods)
			return nil
		}),
	})

	// Start main
	if err := cm.Start(make(chan struct{})); err != nil {
		log.Error(err, "Failed to Start controllerManager")
		os.Exit(1)
	}
}
