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
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

const (
	instanceIDKey = ".spec.instanceID"

	databaseProvider = "Red Hat DBaaS / Amazon Relational Database Service (RDS)"

	connectionConditionReady = "ReadyForBinding"
)

// RDSConnectionReconciler reconciles a RDSConnection object
type RDSConnectionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsconnections,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsconnections/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsconnections/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	var connection rdsdbaasv1alpha1.RDSConnection
	var inventory rdsdbaasv1alpha1.RDSInventory
	var dbInstance rdsv1alpha1.DBInstance

	if err = r.Get(ctx, req.NamespacedName, &connection); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Connection resource not found, has been deleted")
			return
		}
		logger.Error(err, "Error fetching RDS Connection for reconcile")
		return
	}

	if e := r.Get(ctx, client.ObjectKey{Namespace: connection.Spec.InventoryRef.Namespace,
		Name: connection.Spec.InventoryRef.Name}, &inventory); e != nil {
		if errors.IsNotFound(e) {
			logger.Info("RDS Inventory resource not found, may have been deleted")
			//TODO
			return
		}
		logger.Error(e, "Error fetching RDS Inventory for reconcile")
		//TODO
		return
	}

	if condition := apimeta.FindStatusCondition(inventory.Status.Conditions, inventoryConditionReady); condition == nil || condition.Status != metav1.ConditionTrue {
		//TODO
		return
	}

	var instanceName *string
	for _, ins := range inventory.Status.Instances {
		if ins.InstanceID == connection.Spec.InstanceID {
			instanceName = &ins.Name
			break
		}
	}
	if instanceName == nil {
		//TODO
	}

	if e := r.Get(ctx, client.ObjectKey{Namespace: connection.Spec.InventoryRef.Namespace,
		Name: *instanceName}, &dbInstance); e != nil {
		//TODO
	}
	if *dbInstance.Status.DBInstanceStatus != "available" {
		//TODO
	}

	if dbInstance.Spec.MasterUserPassword == nil {
		//TODO
	}
	var secret *v1.Secret
	if e := r.Get(ctx, client.ObjectKey{Namespace: dbInstance.Spec.MasterUserPassword.Namespace,
		Name: dbInstance.Spec.MasterUserPassword.Name}, secret); e != nil {
		//TODO
	}
	if secret == nil {
		//TODO
	}
	if v, ok := secret.Data[dbInstance.Spec.MasterUserPassword.Key]; !ok || len(v) == 0 {
		//TODO
	}
	if v, ok := secret.Data["username"]; !ok || len(v) == 0 {
		//TODO
	}
	userSecret, err := r.createOrUpdateSecret(ctx, &connection, secret, &dbInstance)
	if err != nil {
		//TODO
	}

	if dbInstance.Status.Endpoint == nil {
		//TODO
	}
	dbConfigMap, err := r.createOrUpdateConfigMap(ctx, &connection, &dbInstance)
	if err != nil {
		//TODO
	}

	connection.Status.CredentialsRef = &v1.LocalObjectReference{Name: userSecret.Name}
	connection.Status.ConnectionInfoRef = &v1.LocalObjectReference{Name: dbConfigMap.Name}
	if err := r.Status().Update(ctx, &connection); err != nil {
		if errors.IsConflict(err) {
			logger.Info("Connection modified, retry reconciling")
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Failed to update Connection status")
	}

	//TODO

	return
}

func (r *RDSConnectionReconciler) createOrUpdateSecret(ctx context.Context, connection *rdsdbaasv1alpha1.RDSConnection,
	dbSecret *v1.Secret, dbInstance *rdsv1alpha1.DBInstance) (*v1.Secret, error) {

	secretName := fmt.Sprintf("%s-credentials", connection.Name)
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: connection.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.ObjectMeta.Labels = buildLabels(connection)
		if err := ctrl.SetControllerReference(connection, secret, r.Scheme); err != nil {
			return err
		}
		setSecret(secret, dbSecret, dbInstance)
		return nil
	})
	if err != nil {
		//TODO logger.Error(err, "Failed to create or update secret object for the sql user")
		return nil, err
	}
	return secret, nil
}

func setSecret(secret *v1.Secret, dbSecret *v1.Secret, dbInstance *rdsv1alpha1.DBInstance) {
	data := map[string][]byte{
		"username": []byte(*dbInstance.Spec.MasterUsername),
		"password": dbSecret.Data[dbInstance.Spec.MasterUserPassword.Key],
	}
	secret.Data = data
}

func (r *RDSConnectionReconciler) createOrUpdateConfigMap(ctx context.Context, connection *rdsdbaasv1alpha1.RDSConnection,
	dbInstance *rdsv1alpha1.DBInstance) (*v1.ConfigMap, error) {
	cmName := fmt.Sprintf("%s-configs", connection.Name)
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: connection.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.ObjectMeta.Labels = buildLabels(connection)
		if err := ctrl.SetControllerReference(connection, cm, r.Scheme); err != nil {
			return err
		}
		setConfigMap(cm, dbInstance)
		return nil
	})
	if err != nil {
		//TODO logger.Error(err, "Failed to create or update configmap object for the cluster")
		return nil, err
	}
	return cm, nil
}

func buildLabels(connection *rdsdbaasv1alpha1.RDSConnection) map[string]string {
	return map[string]string{
		"managed-by":               "rds-dbaas-operator",
		"owner":                    connection.Name,
		"owner.kind":               connection.Kind,
		"owner.namespace":          connection.Namespace,
		dbaasv1alpha1.TypeLabelKey: dbaasv1alpha1.TypeLabelValue,
	}
}

func setConfigMap(cm *v1.ConfigMap, dbInstance *rdsv1alpha1.DBInstance) {
	dataMap := map[string]string{
		"type":     *dbInstance.Spec.Engine,
		"provider": databaseProvider,
		"host":     *dbInstance.Status.Endpoint.Address,
		"port":     strconv.FormatInt(*dbInstance.Status.Endpoint.Port, 10),
	}
	if dbInstance.Spec.DBName != nil {
		dataMap["database"] = *dbInstance.Spec.DBName
	}

	cm.Data = dataMap
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//TODO
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&rdsdbaasv1alpha1.RDSConnection{}).
		Complete(r); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &rdsdbaasv1alpha1.RDSConnection{}, instanceIDKey, func(rawObj client.Object) []string {
		connection := rawObj.(*rdsdbaasv1alpha1.RDSConnection)
		instanceID := connection.Spec.InstanceID
		return []string{instanceID}
	}); err != nil {
		return err
	}

	return nil
}
