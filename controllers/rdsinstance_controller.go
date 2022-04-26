/*
Copyright 2022.

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

package controllers

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	opHandler "github.com/operator-framework/operator-lib/handler"
	dbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

// RDSInstanceReconciler reconciles a RDSInstance object
type RDSInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinstances/finalizers,verbs=update
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var instance dbaasv1alpha1.RDSInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Instance resource not found, has been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error fetching RDS Instance for reconcile")

		return ctrl.Result{}, err
	}

	opHandler.SetOwnerAnnotations(&instance, nil)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1alpha1.RDSInstance{}).
		Watches(
			&source.Kind{Type: &rdsv1alpha1.DBInstance{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				return []reconcile.Request{getAnnotationRequests(o)}
			}),
		).
		Complete(r)
}

// Code from operator-lib: https://github.com/operator-framework/operator-lib/blob/d389ad4d93a46dba047b11161b755141fc853098/handler/enqueue_annotation.go#L121
func getAnnotationRequests(object client.Object) reconcile.Request {
	if len(object.GetAnnotations()) == 0 {
		return reconcile.Request{}
	}

	if typeString, ok := object.GetAnnotations()[opHandler.TypeAnnotation]; ok && typeString == "RDSInstance.dbaas.redhat.com" {
		namespacedNameString, ok := object.GetAnnotations()[opHandler.NamespacedNameAnnotation]
		if !ok || strings.TrimSpace(namespacedNameString) == "" {
			return reconcile.Request{}
		}
		nsn := parseNamespacedName(namespacedNameString)
		return reconcile.Request{NamespacedName: nsn}
	}
	return reconcile.Request{}
}

func parseNamespacedName(namespacedNameString string) types.NamespacedName {
	values := strings.SplitN(namespacedNameString, "/", 2)

	switch len(values) {
	case 1:
		return types.NamespacedName{Name: values[0]}
	default:
		return types.NamespacedName{Namespace: values[0], Name: values[1]}
	}
}
