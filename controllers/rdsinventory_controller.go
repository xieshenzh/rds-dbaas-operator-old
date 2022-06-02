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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypesv2 "github.com/aws/aws-sdk-go-v2/service/rds/types"
	ophandler "github.com/operator-framework/operator-lib/handler"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

const (
	rdsInventoryType = "RDSInventory.dbaas.redhat.com"

	inventoryFinalizer = "rds.dbaas.redhat.com/inventory"

	awsAccessKeyID              = "AWS_ACCESS_KEY_ID"
	awsSecretAccessKey          = "AWS_SECRET_ACCESS_KEY" //#nosec G101
	awsRegion                   = "AWS_REGION"
	ackResourceTags             = "ACK_RESOURCE_TAGS"
	ackLogLevel                 = "ACK_LOG_LEVEL"
	ackEnableDevelopmentLogging = "ACK_ENABLE_DEVELOPMENT_LOGGING"
	ackWatchNamespace           = "ACK_WATCH_NAMESPACE"
	awsEndpointUrl              = "AWS_ENDPOINT_URL"

	secretName    = "ack-user-secrets"
	configmapName = "ack-user-config"

	adoptedResourceCRDFile = "services.k8s.aws_adoptedresources.yaml"
	fieldExportCRDFile     = "services.k8s.aws_fieldexports.yaml"
	ackDeploymentName      = "ack-rds-controller"

	adpotedDBInstanceLabelKey   = "rds.dbaas.redhat.com/adopted"
	adpotedDBInstanceLabelValue = "true"

	inventoryConditionReady = "SpecSynced"

	inventoryStatusReasonSyncOK       = "SyncOK"
	inventoryStatusReasonUpdating     = "Updating"
	inventoryStatusReasonDeleting     = "Deleting"
	inventoryStatusReasonInputError   = "InputError"
	inventoryStatusReasonBackendError = "BackendError"
	inventoryStatusReasonNotFound     = "NotFound"

	inventoryStatusMessageUpdating            = "Updating Inventory"
	inventoryStatusMessageUpdateError         = "Failed to update Inventory"
	inventoryStatusMessageDeleting            = "Deleting Inventory"
	inventoryStatusMessageInstalling          = "Installing RDS controller"
	inventoryStatusMessageGetInstancesError   = "Failed to get Instances"
	inventoryStatusMessageAdoptInstanceError  = "Failed to adopt DB Instance"
	inventoryStatusMessageUpdateInstanceError = "Failed to update DB Instance"
	inventoryStatusMessageGetError            = "Failed to get %s"
	inventoryStatusMessageDeleteError         = "Failed to delete %s"
	inventoryStatusMessageCreateOrUpdateError = "Failed to create or update %s"
	inventoryStatusMessageInstallError        = "Failed to install %s for RDS controller"
	inventoryStatusMessageVerifyInstallError  = "Failed to verify %s ready for RDS controller"
	inventoryStatusMessageUninstallError      = "Failed to uninstall RDS controller"

	requiredCredentialErrorTemplate = "required credential %s is missing"
)

// RDSInventoryReconciler reconciles a RDSInventory object
type RDSInventoryReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	ACKInstallNamespace string
	RDSCRDFilePath      string
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinventories,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinventories/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinventories/finalizers,verbs=update
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances/finalizers,verbs=update
//+kubebuilder:rbac:groups=services.k8s.aws,resources=adoptedresources,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSInventoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	var syncStatus, syncStatusReason, syncStatusMessage string

	var inventory rdsdbaasv1alpha1.RDSInventory
	var credentialsRef v1.Secret

	var accessKey, secretKey, region string

	returnUpdating := func() {
		result = ctrl.Result{Requeue: true}
		err = nil
		syncStatus = string(metav1.ConditionUnknown)
		syncStatusReason = inventoryStatusReasonUpdating
		syncStatusMessage = inventoryStatusMessageUpdating
	}

	returnError := func(e error, reason, message string) {
		result = ctrl.Result{}
		err = e
		syncStatus = string(metav1.ConditionFalse)
		syncStatusReason = reason
		syncStatusMessage = message
	}

	returnNotReady := func(reason, message string) {
		result = ctrl.Result{}
		err = nil
		syncStatus = string(metav1.ConditionFalse)
		syncStatusReason = reason
		syncStatusMessage = message
	}

	returnRequeue := func(reason, message string) {
		result = ctrl.Result{Requeue: true}
		err = nil
		syncStatus = string(metav1.ConditionFalse)
		syncStatusReason = reason
		syncStatusMessage = message
	}

	returnReady := func() {
		result = ctrl.Result{}
		err = nil
		syncStatus = string(metav1.ConditionTrue)
		syncStatusReason = inventoryStatusReasonSyncOK
	}

	updateInventoryReadyCondition := func() {
		condition := metav1.Condition{
			Type:    inventoryConditionReady,
			Status:  metav1.ConditionStatus(syncStatus),
			Reason:  syncStatusReason,
			Message: syncStatusMessage,
		}
		apimeta.SetStatusCondition(&inventory.Status.Conditions, condition)
		if e := r.Status().Update(ctx, &inventory); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Inventory modified, retry reconciling")
				result = ctrl.Result{Requeue: true}
			} else {
				logger.Error(e, "Failed to update Inventory status")
				if err == nil {
					err = e
				}
			}
		}
	}

	checkFinalizer := func() bool {
		if inventory.ObjectMeta.DeletionTimestamp.IsZero() {
			if !controllerutil.ContainsFinalizer(&inventory, inventoryFinalizer) {
				controllerutil.AddFinalizer(&inventory, inventoryFinalizer)
				if e := r.Update(ctx, &inventory); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Inventory modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to add finalizer to Inventory")
					returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateError)
					return true
				}
				logger.Info("Finalizer added to Inventory")
				returnNotReady(inventoryStatusReasonUpdating, inventoryStatusMessageUpdating)
				return true
			}
		} else {
			if controllerutil.ContainsFinalizer(&inventory, inventoryFinalizer) {
				adoptedResourceList := &ackv1alpha1.AdoptedResourceList{}
				if e := r.List(ctx, adoptedResourceList, client.InNamespace(inventory.Namespace)); e != nil {
					returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageDeleteError, "AdoptedResource"))
					return true
				}
				deletingAdoptedResource := false
				for i := range adoptedResourceList.Items {
					adoptedResource := adoptedResourceList.Items[i]
					if typeString, ok := adoptedResource.GetAnnotations()[ophandler.TypeAnnotation]; ok && typeString == rdsInventoryType {
						namespacedNameString, ok := adoptedResource.GetAnnotations()[ophandler.NamespacedNameAnnotation]
						if !ok || strings.TrimSpace(namespacedNameString) == "" {
							continue
						}
						nsn := parseNamespacedName(namespacedNameString)
						if nsn.Name == inventory.Name && nsn.Namespace == inventory.Namespace {
							if adoptedResource.ObjectMeta.DeletionTimestamp.IsZero() {
								if e := r.Delete(ctx, &adoptedResource); e != nil {
									returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageDeleteError, "AdoptedResource"))
									return true
								}
							}
							deletingAdoptedResource = true
						}
					}
				}
				if deletingAdoptedResource {
					returnUpdating()
					return true
				}

				if e := r.stopRDSController(ctx, r.Client, false); e != nil {
					returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUninstallError)
					return true
				}

				secret := &v1.Secret{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: secretName}, secret); e != nil {
					if !errors.IsNotFound(e) {
						returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageGetError, "Secret"))
						return true
					}
				} else {
					if e := r.Delete(ctx, secret); e != nil {
						returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageDeleteError, "Secret"))
						return true
					}
					returnUpdating()
					return true
				}
				configmap := &v1.ConfigMap{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: configmapName}, configmap); e != nil {
					if !errors.IsNotFound(e) {
						returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageGetError, "ConfigMap"))
						return true
					}
				} else {
					if e := r.Delete(ctx, configmap); e != nil {
						returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageDeleteError, "ConfigMap"))
						return true
					}
					returnUpdating()
					return true
				}

				controllerutil.RemoveFinalizer(&inventory, inventoryFinalizer)
				if e := r.Update(ctx, &inventory); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Inventory modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to remove finalizer from Inventory")
					returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateError)
					return true
				}
				returnNotReady(inventoryStatusReasonUpdating, inventoryStatusMessageUpdating)
				return true
			}
			// Stop reconciliation as the item is being deleted
			returnNotReady(inventoryStatusReasonDeleting, inventoryStatusMessageDeleting)
			return true
		}
		return false
	}

	validateAWSParameter := func() bool {
		if e := r.Get(ctx, client.ObjectKey{Namespace: inventory.Spec.CredentialsRef.Namespace,
			Name: inventory.Spec.CredentialsRef.Name}, &credentialsRef); e != nil {
			logger.Error(e, "Failed to get credentials reference for Inventory")
			if errors.IsNotFound(e) {
				returnError(e, inventoryStatusReasonNotFound, fmt.Sprintf(inventoryStatusMessageGetError, "Credential"))
			}
			returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageGetError, "Credential"))
			return true
		}

		if ak, ok := credentialsRef.Data[awsAccessKeyID]; !ok || len(ak) == 0 {
			e := fmt.Errorf(requiredCredentialErrorTemplate, awsAccessKeyID)
			returnError(e, inventoryStatusReasonInputError, e.Error())
			return true
		} else {
			accessKey = string(ak)
		}
		if sk, ok := credentialsRef.Data[awsSecretAccessKey]; !ok || len(sk) == 0 {
			e := fmt.Errorf(requiredCredentialErrorTemplate, awsSecretAccessKey)
			returnError(e, inventoryStatusReasonInputError, e.Error())
			return true
		} else {
			secretKey = string(sk)
		}
		if r, ok := credentialsRef.Data[awsRegion]; !ok || len(r) == 0 {
			e := fmt.Errorf(requiredCredentialErrorTemplate, awsRegion)
			returnError(e, inventoryStatusReasonInputError, e.Error())
			return true
		} else {
			region = string(r)
		}
		return false
	}

	installRDSController := func() bool {
		if e := r.createOrUpdateSecret(ctx, &inventory, &credentialsRef); e != nil {
			logger.Error(e, "Failed to create or update secret for Inventory")
			returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageCreateOrUpdateError, "Secret"))
			return true
		}
		if e := r.createOrUpdateConfigMap(ctx, &inventory, &credentialsRef); e != nil {
			logger.Error(e, "Failed to create or update configmap for Inventory")
			returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageCreateOrUpdateError, "ConfigMap"))
			return true
		}

		if e := r.startRDSController(ctx); e != nil {
			logger.Error(e, "Failed to start RDS controller")
			returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageInstallError, "Operator Deployment"))
			return true
		}

		if r, e := r.waitForRDSController(ctx); e != nil {
			logger.Error(e, "Failed to check operator Deployment for RDS controller installation")
			returnError(e, inventoryStatusReasonBackendError, fmt.Sprintf(inventoryStatusMessageVerifyInstallError, "Operator Deployment"))
			return true
		} else if !r {
			returnRequeue(inventoryStatusReasonUpdating, inventoryStatusMessageInstalling)
			return true
		}
		return false
	}

	adoptDBInstances := func() bool {
		var awsDBInstances []rdstypesv2.DBInstance
		awsClient := rds.New(rds.Options{
			Region:      region,
			Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		})
		paginator := rds.NewDescribeDBInstancesPaginator(awsClient, nil)
		for paginator.HasMorePages() {
			if output, e := paginator.NextPage(ctx); e != nil {
				logger.Error(e, "Failed to read DB Instances of the Inventory from AWS")
				returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageGetInstancesError)
				return true
			} else if output != nil {
				awsDBInstances = append(awsDBInstances, output.DBInstances...)
			}
		}

		awsDBInstanceMap := make(map[string]rdstypesv2.DBInstance, len(awsDBInstances))
		if len(awsDBInstances) > 0 {
			// query all db instances in cluster
			clusterDBInstanceList := &rdsv1alpha1.DBInstanceList{}
			if e := r.List(ctx, clusterDBInstanceList, client.InNamespace(inventory.Namespace)); e != nil {
				logger.Error(e, "Failed to read DB Instances of the Inventory in the cluster")
				returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageGetInstancesError)
				return true
			}

			dbInstanceMap := make(map[string]rdsv1alpha1.DBInstance, len(clusterDBInstanceList.Items))
			for _, dbInstance := range clusterDBInstanceList.Items {
				dbInstanceMap[*dbInstance.Spec.DBInstanceIdentifier] = dbInstance
			}

			adoptedResourceList := &ackv1alpha1.AdoptedResourceList{}
			if e := r.List(ctx, adoptedResourceList, client.InNamespace(inventory.Namespace)); e != nil {
				logger.Error(e, "Failed to read adopted DB Instances of the Inventory in the cluster")
				returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageGetInstancesError)
				return true
			}
			adoptedDBInstanceMap := make(map[string]ackv1alpha1.AdoptedResource, len(adoptedResourceList.Items))
			for _, adoptedDBInstance := range adoptedResourceList.Items {
				adoptedDBInstanceMap[adoptedDBInstance.Spec.AWS.NameOrID] = adoptedDBInstance
			}

			adoptingResource := false
			for i := range awsDBInstances {
				dbInstance := awsDBInstances[i]
				awsDBInstanceMap[*dbInstance.DBInstanceIdentifier] = dbInstance
				if dbInstance.DBInstanceStatus != nil && *dbInstance.DBInstanceStatus == "deleting" {
					continue
				}
				if _, ok := dbInstanceMap[*dbInstance.DBInstanceIdentifier]; !ok {
					if _, ok := adoptedDBInstanceMap[*dbInstance.DBInstanceIdentifier]; !ok {
						adoptedDBInstance := createAdoptedResource(&dbInstance, &inventory)
						if e := ophandler.SetOwnerAnnotations(&inventory, adoptedDBInstance); e != nil {
							logger.Error(e, "Failed to create adopted DB Instance in the cluster")
							returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageAdoptInstanceError)
							return true
						}
						if e := r.Create(ctx, adoptedDBInstance); e != nil {
							logger.Error(e, "Failed to create adopted DB Instance in the cluster")
							returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageAdoptInstanceError)
							return true
						}
						adoptingResource = true
					}
				}
			}
			if adoptingResource {
				logger.Info("DB Instance adopted")
				returnUpdating()
				return true
			}
		}

		adoptedDBInstanceList := &rdsv1alpha1.DBInstanceList{}
		if e := r.List(ctx, adoptedDBInstanceList, client.InNamespace(inventory.Namespace),
			client.MatchingLabels(map[string]string{adpotedDBInstanceLabelKey: adpotedDBInstanceLabelValue})); e != nil {
			logger.Error(e, "Failed to read adopted DB Instances of the Inventory that are created by the operator")
			returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageGetInstancesError)
			return true
		}
		waitForAdoptedResource := false
		for i := range adoptedDBInstanceList.Items {
			adoptedDBInstance := adoptedDBInstanceList.Items[i]
			if adoptedDBInstance.Spec.MasterUserPassword == nil {
				if adoptedDBInstance.Status.DBInstanceStatus != nil && *adoptedDBInstance.Status.DBInstanceStatus != "available" {
					waitForAdoptedResource = true
					continue
				}
				s, e := setCredentials(ctx, r.Client, r.Scheme, &adoptedDBInstance, inventory.Namespace, &adoptedDBInstance, adoptedDBInstance.Kind)
				if e != nil {
					returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateInstanceError)
					return true
				}
				password := s.Data["password"]
				input := &rds.ModifyDBInstanceInput{
					DBInstanceIdentifier: adoptedDBInstance.Spec.DBInstanceIdentifier,
					MasterUserPassword:   pointer.String(string(password)),
				}
				if _, e := awsClient.ModifyDBInstance(ctx, input); e != nil {
					logger.Error(e, "Failed to update credentials of the adopted DB Instance", "DB Instance", adoptedDBInstance)
					returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateInstanceError)
					return true
				}
				if e := r.Update(ctx, &adoptedDBInstance); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Adopted DB Instance modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to update credentials of the adopted DB Instance", "DB Instance", adoptedDBInstance)
					returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateInstanceError)
					return true
				}
			}

			if adoptedDBInstance.Spec.MasterUsername == nil || adoptedDBInstance.Spec.DBName == nil {
				if awsDBInstance, ok := awsDBInstanceMap[*adoptedDBInstance.Spec.DBInstanceIdentifier]; ok {
					update := false
					if adoptedDBInstance.Spec.MasterUsername == nil && awsDBInstance.MasterUsername != nil {
						adoptedDBInstance.Spec.MasterUsername = pointer.String(*awsDBInstance.MasterUsername)
						update = true
					}
					if adoptedDBInstance.Spec.DBName == nil && awsDBInstance.DBName != nil {
						adoptedDBInstance.Spec.DBName = pointer.String(*awsDBInstance.DBName)
						update = true
					}
					if update {
						if e := r.Update(ctx, &adoptedDBInstance); e != nil {
							if errors.IsConflict(e) {
								logger.Info("Adopted DB Instance modified, retry reconciling")
								returnUpdating()
								return true
							}
							logger.Error(e, "Failed to update connection info of the adopted DB Instance", "DB Instance", adoptedDBInstance)
							returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateInstanceError)
							return true
						}
					}
				}
			}
		}
		if waitForAdoptedResource {
			logger.Info("DB Instance being adopted is not available, retry reconciling")
			returnUpdating()
			return true
		}
		return false
	}

	syncDBInstancesStatus := func() bool {
		dbInstanceList := &rdsv1alpha1.DBInstanceList{}
		if e := r.List(ctx, dbInstanceList, client.InNamespace(inventory.Namespace)); e != nil {
			logger.Error(e, "Failed to read DB Instances of the Inventory in the cluster")
			returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageGetInstancesError)
			return true
		}

		var instances []dbaasv1alpha1.Instance
		for i := range dbInstanceList.Items {
			dbInstance := dbInstanceList.Items[i]
			instance := dbaasv1alpha1.Instance{
				InstanceID:   *dbInstance.Spec.DBInstanceIdentifier,
				Name:         dbInstance.Name,
				InstanceInfo: parseDBInstanceStatus(&dbInstance),
			}
			instances = append(instances, instance)
		}
		inventory.Status.Instances = instances

		if e := r.Status().Update(ctx, &inventory); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Inventory modified, retry reconciling")
				returnUpdating()
				return true
			}
			logger.Error(e, "Failed to update Inventory status")
			returnError(e, inventoryStatusReasonBackendError, inventoryStatusMessageUpdateError)
			return true
		}

		return false
	}

	if err = r.Get(ctx, req.NamespacedName, &inventory); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Inventory resource not found, has been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error fetching RDS Inventory for reconcile")
		return ctrl.Result{}, err
	}

	defer updateInventoryReadyCondition()

	if checkFinalizer() {
		return
	}

	if validateAWSParameter() {
		return
	}

	if installRDSController() {
		return
	}

	if adoptDBInstances() {
		return
	}

	if syncDBInstancesStatus() {
		return
	}

	returnReady()
	return
}

func (r *RDSInventoryReconciler) createOrUpdateSecret(ctx context.Context, inventory *rdsdbaasv1alpha1.RDSInventory,
	credentialsRef *v1.Secret) error {
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: r.ACKInstallNamespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.ObjectMeta.Labels = buildInventoryLabels(inventory)
		if e := ophandler.SetOwnerAnnotations(inventory, secret); e != nil {
			return e
		}
		secret.Data = map[string][]byte{
			awsAccessKeyID:     credentialsRef.Data[awsAccessKeyID],
			awsSecretAccessKey: credentialsRef.Data[awsSecretAccessKey],
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *RDSInventoryReconciler) createOrUpdateConfigMap(ctx context.Context, inventory *rdsdbaasv1alpha1.RDSInventory,
	credentialsRef *v1.Secret) error {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configmapName,
			Namespace: r.ACKInstallNamespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.ObjectMeta.Labels = buildInventoryLabels(inventory)
		if e := ophandler.SetOwnerAnnotations(inventory, cm); e != nil {
			return e
		}
		cm.Data = map[string]string{
			awsRegion:                   string(credentialsRef.Data[awsRegion]),
			awsEndpointUrl:              "",
			ackEnableDevelopmentLogging: "false",
			ackWatchNamespace:           "",
			ackLogLevel:                 "info",
			ackResourceTags:             "rhoda",
		}
		if l, ok := credentialsRef.Data[ackLogLevel]; ok {
			cm.Data[ackLogLevel] = string(l)
		}
		if t, ok := credentialsRef.Data[ackResourceTags]; ok {
			cm.Data[ackResourceTags] = string(t)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func buildInventoryLabels(inventory *rdsdbaasv1alpha1.RDSInventory) map[string]string {
	return map[string]string{
		"managed-by":               "rds-dbaas-operator",
		"owner":                    inventory.Name,
		"owner.kind":               inventory.Kind,
		"owner.namespace":          inventory.Namespace,
		dbaasv1alpha1.TypeLabelKey: dbaasv1alpha1.TypeLabelValue,
	}
}

func (r *RDSInventoryReconciler) installCRD(ctx context.Context, cli client.Client, file string) error {
	crd, err := r.readCRDFile(file)
	if err != nil {
		return err
	}
	c := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crd.Name,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, cli, c, func() error {
		c.Spec = crd.Spec
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (r *RDSInventoryReconciler) readCRDFile(file string) (*apiextensionsv1.CustomResourceDefinition, error) {
	d, err := ioutil.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, err
	}
	jsonData, err := yaml.ToJSON(d)
	if err != nil {
		return nil, err
	}
	csv := &apiextensionsv1.CustomResourceDefinition{}
	if err := json.Unmarshal(jsonData, csv); err != nil {
		return nil, err
	}

	return csv, nil
}

func (r *RDSInventoryReconciler) waitForRDSController(ctx context.Context) (bool, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: ackDeploymentName}, deployment); err != nil {
		return false, err
	}
	if deployment.Status.ReadyReplicas > 0 {
		return true, nil
	}
	return false, nil
}

func (r *RDSInventoryReconciler) stopRDSController(ctx context.Context, cli client.Client, wait bool) error {
	logger := log.FromContext(ctx)

	deployment := &appsv1.Deployment{}
	for {
		if err := cli.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: ackDeploymentName}, deployment); err != nil {
			if errors.IsNotFound(err) {
				if wait {
					logger.Info("Wait for the installation of the RDS controller")
					time.Sleep(25 * time.Second)
					continue
				} else {
					return err
				}
			}
			return err
		}
		break
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas > 0 {
		deployment.Spec.Replicas = pointer.Int32(0)
		if err := cli.Update(ctx, deployment); err != nil {
			return err
		}
	}
	return nil
}

func (r *RDSInventoryReconciler) startRDSController(ctx context.Context) error {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: ackDeploymentName}, deployment); err != nil {
		return err
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
		deployment.Spec.Replicas = pointer.Int32(1)
		if err := r.Update(ctx, deployment); err != nil {
			return err
		}
	}
	return nil
}

func createAdoptedResource(dbInstance *rdstypesv2.DBInstance, inventory *rdsdbaasv1alpha1.RDSInventory) *ackv1alpha1.AdoptedResource {
	return &ackv1alpha1.AdoptedResource{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    inventory.Namespace,
			GenerateName: fmt.Sprintf("%s-", *dbInstance.DBInstanceIdentifier),
			Labels: map[string]string{
				"managed-by":      "rds-dbaas-operator",
				"owner":           inventory.Name,
				"owner.kind":      inventory.Kind,
				"owner.namespace": inventory.Namespace,
			},
		},
		Spec: ackv1alpha1.AdoptedResourceSpec{
			Kubernetes: &ackv1alpha1.ResourceWithMetadata{
				GroupKind: metav1.GroupKind{
					Group: rdsv1alpha1.GroupVersion.Group,
					Kind:  rdsInstanceKind,
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
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSInventoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	kubeConfig := mgr.GetConfig()
	cli, err := client.New(kubeConfig, client.Options{
		Scheme: mgr.GetScheme(),
	})
	if err != nil {
		return err
	}
	if err := r.stopRDSController(ctx, cli, true); err != nil {
		return err
	}

	if err := r.installCRD(ctx, cli, filepath.Join(r.RDSCRDFilePath, adoptedResourceCRDFile)); err != nil {
		return err
	}
	if err := r.installCRD(ctx, cli, filepath.Join(r.RDSCRDFilePath, fieldExportCRDFile)); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rdsdbaasv1alpha1.RDSInventory{}).
		Watches(
			&source.Kind{Type: &rdsv1alpha1.DBInstance{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				return getInstanceInventoryRequests(o, mgr)
			}),
		).
		Complete(r)
}

func getInstanceInventoryRequests(object client.Object, mgr ctrl.Manager) []reconcile.Request {
	ctx := context.Background()
	logger := log.FromContext(ctx)
	cli := mgr.GetClient()

	dbInstance := object.(*rdsv1alpha1.DBInstance)
	inventoryList := &rdsdbaasv1alpha1.RDSInventoryList{}
	if e := cli.List(ctx, inventoryList, client.InNamespace(dbInstance.Namespace)); e != nil {
		logger.Error(e, "Failed to get Inventories for DB Instance update", "DBInstance ID", dbInstance.Spec.DBInstanceIdentifier)
		return nil
	}

	var requests []reconcile.Request
	for _, i := range inventoryList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: i.Namespace,
				Name:      i.Name,
			},
		})
	}
	return requests
}
