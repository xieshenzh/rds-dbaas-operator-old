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
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcache "github.com/aws-controllers-k8s/runtime/pkg/runtime/cache"
	rdstypesv2 "github.com/aws/aws-sdk-go-v2/service/rds/types"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

const (
	adpotedDBInstanceLabelKey   = "rds.dbaas.redhat.com/adopted"
	adpotedDBInstanceLabelValue = "true"

	inventoryConditionReady = "SpecSynced"

	inventoryStatusMessageSyncOK              = "SyncOK"
	inventoryStatusMessageInputError          = "InputError"
	inventoryStatusMessageBackendError        = "BackendError"
	inventoryStatusMessageEndpointUnreachable = "EndpointUnreachable"
	inventoryStatusMessageAuthenticationError = "AuthenticationError"
)

// RDSInventoryReconciler reconciles a RDSInventory object
type RDSInventoryReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	ACKInstallNamespace string
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinventories,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinventories/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinventories/finalizers,verbs=update
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch
//+kubebuilder:rbac:groups=services.k8s.aws,resources=adoptedresources,verbs=get;list;create
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch;update
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSInventoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	var inventory rdsdbaasv1alpha1.RDSInventory

	if err = r.Get(ctx, req.NamespacedName, &inventory); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Inventory resource not found, has been deleted")
			return
		}
		logger.Error(err, "Error fetching RDS Inventory for reconcile")
		return
	}

	var roleARN *string
	inventoryNamespace := &v1.Namespace{}
	if e := r.Get(ctx, client.ObjectKey{Name: inventory.Namespace}, inventoryNamespace); e != nil {
		//TODO
	}
	if ownerAccountID, ok := inventoryNamespace.Annotations[ackv1alpha1.AnnotationOwnerAccountID]; ok {
		roleAccountMap := &v1.ConfigMap{}
		if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: ackcache.ACKRoleAccountMap}, roleAccountMap); e != nil {
			//TODO
		}
		if arn, ok := roleAccountMap.Data[ownerAccountID]; ok {
			roleARN = &arn
		}
	}

	awsDBInstances := []rdstypesv2.DBInstance{}

	// query all db instances in cluster
	clusterDBInstanceList := &rdsv1alpha1.DBInstanceList{}
	if e := r.List(ctx, clusterDBInstanceList, client.InNamespace(inventory.Namespace)); e != nil {
		//TODO
	}

	dbInstanceMap := make(map[string]rdsv1alpha1.DBInstance, len(clusterDBInstanceList.Items))
	for _, dbInstance := range clusterDBInstanceList.Items {
		dbInstanceMap[*dbInstance.Spec.DBInstanceIdentifier] = dbInstance
	}

	adoptedResourceList := &ackv1alpha1.AdoptedResourceList{}
	if e := r.List(ctx, adoptedResourceList, client.InNamespace(inventory.Namespace)); e != nil {
		//TODO
	}
	adoptedDBInstanceMap := make(map[string]ackv1alpha1.AdoptedResource, len(adoptedResourceList.Items))
	for _, adoptedDBInstance := range adoptedResourceList.Items {
		adoptedDBInstanceMap[adoptedDBInstance.Spec.AWS.NameOrID] = adoptedDBInstance
	}

	dbInstanceGVK := (&rdsv1alpha1.DBInstance{}).GroupVersionKind()
	inventoryGVK := inventory.GroupVersionKind()
	for _, dbInstance := range awsDBInstances {
		if _, ok := dbInstanceMap[*dbInstance.DBInstanceIdentifier]; !ok {
			if _, ok := adoptedDBInstanceMap[*dbInstance.DBInstanceIdentifier]; !ok {
				adoptedDBInstance := &ackv1alpha1.AdoptedResource{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:    inventory.Namespace,
						GenerateName: fmt.Sprintf("%s-", *dbInstance.DBInstanceIdentifier),
						Labels: map[string]string{
							"managed-by":      "rds-dbaas-operator",
							"owner":           inventory.Name,
							"owner.kind":      inventory.Kind,
							"owner.namespace": inventory.Namespace,
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         inventoryGVK.GroupVersion().String(),
								Kind:               inventoryGVK.Kind,
								Name:               inventory.GetName(),
								UID:                inventory.GetUID(),
								BlockOwnerDeletion: pointer.BoolPtr(true),
								Controller:         pointer.BoolPtr(true),
							},
						},
					},
					Spec: ackv1alpha1.AdoptedResourceSpec{
						Kubernetes: &ackv1alpha1.ResourceWithMetadata{
							GroupKind: metav1.GroupKind{
								Group: dbInstanceGVK.Group,
								Kind:  dbInstanceGVK.Kind,
							},
							Metadata: &ackv1alpha1.PartialObjectMeta{
								Namespace: inventory.Namespace,
								Labels: map[string]string{
									adpotedDBInstanceLabelKey: adpotedDBInstanceLabelValue,
								},
							},
						},
						AWS: &ackv1alpha1.AWSIdentifiers{
							NameOrID: *dbInstance.DBInstanceIdentifier,
						},
					},
				}
				if e := r.Create(ctx, adoptedDBInstance); e != nil {
					//TODO
				}
			}
		}
	}

	//TODO update password
	adoptedDBInstanceList := &rdsv1alpha1.DBInstanceList{}
	if e := r.List(ctx, adoptedDBInstanceList, client.InNamespace(inventory.Namespace),
		client.MatchingLabels(map[string]string{adpotedDBInstanceLabelKey: adpotedDBInstanceLabelValue})); e != nil {
		//TODO
	}

	for _, adoptedDBInstance := range adoptedDBInstanceList.Items {
		if adoptedDBInstance.Spec.MasterUsername == nil || adoptedDBInstance.Spec.MasterUserPassword == nil {
			if e := setCredentials(ctx, r, r.Scheme, &adoptedDBInstance, inventory.Namespace, nil, ""); e != nil {
				//TODO
			}
			if e := r.Update(ctx, &adoptedDBInstance); e != nil {
				if errors.IsConflict(e) {
					//TODO
				}
				//TODO
			}
		}
	}

	dbInstanceList := &rdsv1alpha1.DBInstanceList{}
	if e := r.List(ctx, dbInstanceList, client.InNamespace(inventory.Namespace)); e != nil {
		//TODO
	}

	var instances []dbaasv1alpha1.Instance
	for _, dbInstance := range dbInstanceList.Items {
		instance := dbaasv1alpha1.Instance{
			InstanceID:   *dbInstance.Spec.DBInstanceIdentifier,
			Name:         dbInstance.Name,
			InstanceInfo: parseDBInstanceStatus(&dbInstance),
		}
		instances = append(instances, instance)
	}
	inventory.Status.Instances = instances

	//TODO phase

	if e := r.Status().Update(ctx, &inventory); e != nil {
		if errors.IsConflict(e) {
			logger.Info("Inventory modified, retry reconciling")
			//TODO
			return
		}
		logger.Error(e, "Failed to update Inventory status")
		//TODO
		return
	}

	return
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSInventoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rdsdbaasv1alpha1.RDSInventory{}).
		Watches(
			&source.Kind{Type: &rdsv1alpha1.DBInstance{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				return []reconcile.Request{} //TODO
			}),
		).
		Complete(r)
}
