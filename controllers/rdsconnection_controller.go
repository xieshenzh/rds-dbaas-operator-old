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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

const (
	instanceIDKey = ".spec.instanceID"

	databaseProvider = "Red Hat DBaaS / Amazon Relational Database Service (RDS)"

	connectionConditionReady = "ReadyForBinding"

	connectionStatusReasonReady        = "Ready"
	connectionStatusReasonUpdating     = "Updating"
	connectionStatusReasonBackendError = "BackendError"
	connectionStatusReasonInputError   = "InputError"
	connectionStatusReasonNotFound     = "NotFound"
	connectionStatusReasonUnreachable  = "Unreachable"

	connectionStatusMessageUpdateError       = "Failed to update Connection"
	connectionStatusMessageUpdating          = "Updating Connection"
	connectionStatusMessageSecretError       = "Failed to create or update secret"
	connectionStatusMessageConfigMapError    = "Failed to create or update configmap"
	connectionStatusMessageInstanceNotFound  = "Instance not found"
	connectionStatusMessageInstanceNotReady  = "Instance not ready"
	connectionStatusMessageGetInstanceError  = "Failed to get Instance"
	connectionStatusMessagePasswordNotFound  = "Password not found"
	connectionStatusMessagePasswordInvalid   = "Password invalid"
	connectionStatusMessageUsernameNotFound  = "Username not found"
	connectionStatusMessageEndpointNotFound  = "Endpoint not found"
	connectionStatusMessageGetPasswordError  = "Failed to get secret for password" //#nosec G101
	connectionStatusMessageInventoryNotFound = "Inventory not found"
	connectionStatusMessageInventoryNotReady = "Inventory not ready"
	connectionStatusMessageGetInventoryError = "Failed to get Inventory"
)

// RDSConnectionReconciler reconciles a RDSConnection object
type RDSConnectionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsconnections,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsconnections/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsconnections/finalizers,verbs=update
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch;create;delete;update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	var bindingStatus, bindingStatusReason, bindingStatusMessage string

	var connection rdsdbaasv1alpha1.RDSConnection
	var inventory rdsdbaasv1alpha1.RDSInventory
	var dbInstance rdsv1alpha1.DBInstance

	var masterUserSecret v1.Secret

	returnError := func(e error, reason, message string) {
		result = ctrl.Result{}
		err = e
		bindingStatus = string(metav1.ConditionFalse)
		bindingStatusReason = reason
		bindingStatusMessage = message
	}

	returnRequeue := func(reason, message string) {
		result = ctrl.Result{Requeue: true}
		err = nil
		bindingStatus = string(metav1.ConditionFalse)
		bindingStatusReason = reason
		bindingStatusMessage = message
	}

	returnReady := func() {
		result = ctrl.Result{}
		err = nil
		bindingStatus = string(metav1.ConditionTrue)
		bindingStatusReason = connectionStatusReasonReady
	}

	updateConnectionReadyCondition := func() {
		condition := metav1.Condition{
			Type:    connectionConditionReady,
			Status:  metav1.ConditionStatus(bindingStatus),
			Reason:  bindingStatusReason,
			Message: bindingStatusMessage,
		}
		apimeta.SetStatusCondition(&connection.Status.Conditions, condition)
		if e := r.Status().Update(ctx, &connection); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Connection modified, retry reconciling")
				result = ctrl.Result{Requeue: true}
			} else {
				logger.Error(e, "Failed to update Connection status")
				if err == nil {
					err = e
				}
			}
		}
	}

	checkDBInstanceStatus := func() bool {
		var instanceName *string
		for _, ins := range inventory.Status.Instances {
			if ins.InstanceID == connection.Spec.InstanceID {
				instanceName = &ins.Name
				break
			}
		}
		if instanceName == nil {
			e := fmt.Errorf("instance %s not found", connection.Spec.InstanceID)
			logger.Error(e, "DB Instance not found from Inventory")
			returnError(e, connectionStatusReasonNotFound, connectionStatusMessageInstanceNotFound)
			return true
		}

		if e := r.Get(ctx, client.ObjectKey{Namespace: connection.Spec.InventoryRef.Namespace,
			Name: *instanceName}, &dbInstance); e != nil {
			logger.Error(e, "Failed to get DB Instance")
			returnError(e, connectionStatusReasonBackendError, connectionStatusMessageGetInstanceError)
			return true
		}
		if *dbInstance.Status.DBInstanceStatus != "available" {
			e := fmt.Errorf("instance %s not ready", connection.Spec.InstanceID)
			logger.Error(e, "DB Instance not ready")
			returnError(e, connectionStatusReasonUnreachable, connectionStatusMessageInstanceNotReady)
			return true
		}
		return false
	}

	checkDBConnectionStatus := func() bool {
		if dbInstance.Spec.MasterUserPassword == nil {
			e := fmt.Errorf("instance %s master password not set", connection.Spec.InstanceID)
			logger.Error(e, "DB Instance master password not set")
			returnError(e, connectionStatusReasonInputError, connectionStatusMessagePasswordNotFound)
			return true
		}

		if e := r.Get(ctx, client.ObjectKey{Namespace: dbInstance.Spec.MasterUserPassword.Namespace,
			Name: dbInstance.Spec.MasterUserPassword.Name}, &masterUserSecret); e != nil {
			logger.Error(e, "Failed to get secret for DB Instance master password")
			if errors.IsNotFound(e) {
				returnError(e, connectionStatusReasonNotFound, connectionStatusMessageGetPasswordError)
			} else {
				returnError(e, connectionStatusReasonBackendError, connectionStatusMessageGetPasswordError)
			}
			return true
		}
		if v, ok := masterUserSecret.Data[dbInstance.Spec.MasterUserPassword.Key]; !ok || len(v) == 0 {
			e := fmt.Errorf("instance %s master password key not set", connection.Spec.InstanceID)
			logger.Error(e, "DB Instance master password key not set")
			returnError(e, connectionStatusReasonInputError, connectionStatusMessagePasswordInvalid)
			return true
		}
		if dbInstance.Spec.MasterUsername == nil {
			e := fmt.Errorf("instance %s master username not set", connection.Spec.InstanceID)
			logger.Error(e, "DB Instance master username not set")
			returnError(e, connectionStatusReasonInputError, connectionStatusMessageUsernameNotFound)
			return true
		}

		if dbInstance.Status.Endpoint == nil {
			e := fmt.Errorf("instance %s endpoint not found", connection.Spec.InstanceID)
			logger.Error(e, "DB Instance endpoint not found")
			returnError(e, connectionStatusReasonUnreachable, connectionStatusMessageEndpointNotFound)
			return true
		}
		return false
	}

	syncConnectionStatus := func() bool {
		userSecret, e := r.createOrUpdateSecret(ctx, &connection, &masterUserSecret, &dbInstance)
		if e != nil {
			logger.Error(e, "Failed to create or update secret for Connection")
			returnError(e, connectionStatusReasonBackendError, connectionStatusMessageSecretError)
			return true
		}

		dbConfigMap, e := r.createOrUpdateConfigMap(ctx, &connection, &dbInstance)
		if e != nil {
			logger.Error(e, "Failed to create or update configmap for Connection")
			returnError(e, connectionStatusReasonBackendError, connectionStatusMessageConfigMapError)
			return true
		}

		connection.Status.CredentialsRef = &v1.LocalObjectReference{Name: userSecret.Name}
		connection.Status.ConnectionInfoRef = &v1.LocalObjectReference{Name: dbConfigMap.Name}
		if e := r.Status().Update(ctx, &connection); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Connection modified, retry reconciling")
				returnRequeue(connectionStatusReasonUpdating, connectionStatusMessageUpdating)
				return true
			}
			logger.Error(e, "Failed to update Connection status")
			returnError(e, connectionStatusReasonBackendError, connectionStatusMessageUpdateError)
			return true
		}
		return false
	}

	if err = r.Get(ctx, req.NamespacedName, &connection); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Connection resource not found, has been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error fetching RDS Connection for reconcile")
		return ctrl.Result{}, err
	}

	defer updateConnectionReadyCondition()

	if e := r.Get(ctx, client.ObjectKey{Namespace: connection.Spec.InventoryRef.Namespace,
		Name: connection.Spec.InventoryRef.Name}, &inventory); e != nil {
		if errors.IsNotFound(e) {
			logger.Info("RDS Inventory resource not found, may have been deleted")
			returnError(e, connectionStatusReasonNotFound, connectionStatusMessageInventoryNotFound)
			return
		}
		logger.Error(e, "Failed to get RDS Inventory")
		returnError(e, connectionStatusReasonBackendError, connectionStatusMessageGetInventoryError)
		return
	}

	if condition := apimeta.FindStatusCondition(inventory.Status.Conditions, inventoryConditionReady); condition == nil || condition.Status != metav1.ConditionTrue {
		logger.Info("RDS Inventory not ready")
		returnRequeue(connectionStatusReasonUnreachable, connectionStatusMessageInventoryNotReady)
		return
	}

	if checkDBInstanceStatus() {
		return
	}

	if checkDBConnectionStatus() {
		return
	}

	if syncConnectionStatus() {
		return
	}

	returnReady()
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
		secret.ObjectMeta.Labels = buildConnectionLabels(connection)
		if err := ctrl.SetControllerReference(connection, secret, r.Scheme); err != nil {
			return err
		}
		setSecret(secret, dbSecret, dbInstance)
		return nil
	})
	if err != nil {
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
		cm.ObjectMeta.Labels = buildConnectionLabels(connection)
		if err := ctrl.SetControllerReference(connection, cm, r.Scheme); err != nil {
			return err
		}
		setConfigMap(cm, dbInstance)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func buildConnectionLabels(connection *rdsdbaasv1alpha1.RDSConnection) map[string]string {
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
		"type":     generateBindingType(*dbInstance.Spec.Engine),
		"provider": databaseProvider,
		"host":     *dbInstance.Status.Endpoint.Address,
		"port":     strconv.FormatInt(*dbInstance.Status.Endpoint.Port, 10),
	}
	if dbInstance.Spec.DBName != nil {
		dataMap["database"] = *dbInstance.Spec.DBName
	} else {
		switch *dbInstance.Spec.Engine {
		case "sqlserver-ee", "sqlserver-se", "sqlserver-ex", "sqlserver-web":
			dataMap["database"] = "master"
		case "mysql", "mariadb", "aurora", "aurora-mysql":
			dataMap["database"] = "mysql"
		case "postgres", "aurora-postgresql":
			dataMap["database"] = "postgres"
		case "oracle-se2", "oracle-se2-cdb", "oracle-ee", "oracle-ee-cdb", "custom-oracle-ee":
			dataMap["database"] = "ORCL"
		default:
		}
	}

	cm.Data = dataMap
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&rdsdbaasv1alpha1.RDSConnection{}).
		Watches(
			&source.Kind{Type: &rdsv1alpha1.DBInstance{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				return getInstanceConnectionRequests(o, mgr)
			}),
		).
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

func getInstanceConnectionRequests(object client.Object, mgr ctrl.Manager) []reconcile.Request {
	ctx := context.Background()
	logger := log.FromContext(ctx)
	cli := mgr.GetClient()

	dbInstance := object.(*rdsv1alpha1.DBInstance)
	connectionList := &rdsdbaasv1alpha1.RDSConnectionList{}
	if e := cli.List(ctx, connectionList, client.MatchingFields{instanceIDKey: *dbInstance.Spec.DBInstanceIdentifier}); e != nil {
		logger.Error(e, "Failed to get Connections for DB Instance update", "DBInstance ID", dbInstance.Spec.DBInstanceIdentifier)
		return nil
	}

	var requests []reconcile.Request
	for _, c := range connectionList.Items {
		match := false
		if len(c.Spec.InventoryRef.Namespace) > 0 {
			if c.Spec.InventoryRef.Namespace == dbInstance.Namespace {
				match = true
			}
		} else {
			if c.Namespace == dbInstance.Namespace {
				match = true
			}
		}
		if match {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: c.Namespace,
					Name:      c.Name,
				},
			})
		}
	}
	return requests
}
