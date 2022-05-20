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
	"regexp"
	"strconv"
	"strings"

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
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ophandler "github.com/operator-framework/operator-lib/handler"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

const (
	rdsInstanceType = "RDSInstance.dbaas.redhat.com"

	instanceFinalizer = "rds.dbaas.redhat.com/instance"

	engine               = "Engine"
	engineVersion        = "EngineVersion"
	dbInstanceIdentifier = "DBInstanceIdentifier"
	dbInstanceClass      = "DBInstanceClass"
	storageType          = "StorageType"
	allocatedStorage     = "AllocatedStorage"
	iops                 = "IOPS"
	maxAllocatedStorage  = "MaxAllocatedStorage"
	dbSubnetGroupName    = "DBSubnetGroupName"
	publiclyAccessible   = "PubliclyAccessible"
	vpcSecurityGroupIDs  = "VPCSecurityGroupIDs"

	instanceConditionReady = "ProvisionReady"

	instanceStatusReasonReady        = "Ready"
	instanceStatusReasonCreating     = "Creating"
	instanceStatusReasonUpdating     = "Updating"
	instanceStatusReasonDeleting     = "Deleting"
	instanceStatusReasonTerminated   = "Terminated"
	instanceStatusReasonInputError   = "InputError"
	instanceStatusReasonBackendError = "BackendError"
	instanceStatusReasonNotFound     = "NotFound"
	instanceStatusReasonUnreachable  = "Unreachable"
	instanceStatusReasonDBInstance   = "DBInstance"

	instanceStatusMessageUpdateError         = "Failed to update Instance"
	instanceStatusMessageCreating            = "Creating Instance"
	instanceStatusMessageUpdating            = "Updating Instance"
	instanceStatusMessageDeleting            = "Deleting Instance"
	instanceStatusMessageError               = "Instance with error"
	instanceStatusMessageCreateOrUpdateError = "Failed to create or update DB Instance"
	instanceStatusMessageGetError            = "Failed to get DB Instance"
	instanceStatusMessageDeleteError         = "Failed to delete DB Instance"
	instanceStatusMessageInventoryNotFound   = "Inventory not found"
	instanceStatusMessageInventoryNotReady   = "Inventory not ready"
	instanceStatusMessageGetInventoryError   = "Failed to get Inventory"

	instancePhasePending  = "Pending"
	instancePhaseCreating = "Creating"
	instancePhaseUpdating = "Updating"
	instancePhaseDeleting = "Deleting"
	instancePhaseDeleted  = "Deleted"
	instancePhaseReady    = "Ready"
	instancePhaseError    = "Error"
	instancePhaseFailed   = "Failed"
	instancePhaseUnknown  = "Unknown"

	requiredParameterErrorTemplate = "required parameter %s is missing"
	invalidParameterErrorTemplate  = "value of parameter %s is invalid"
)

// RDSInstanceReconciler reconciles a RDSInstance object
type RDSInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=rdsinstances/finalizers,verbs=update
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch;create;update;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	var inventory rdsdbaasv1alpha1.RDSInventory
	var instance rdsdbaasv1alpha1.RDSInstance

	var provisionStatus, provisionStatusReason, provisionStatusMessage, phase string

	returnUpdating := func() {
		result = ctrl.Result{Requeue: true}
		err = nil
		provisionStatus = string(metav1.ConditionUnknown)
		provisionStatusReason = instanceStatusReasonUpdating
		provisionStatusMessage = instanceStatusMessageUpdating
	}

	returnError := func(e error, reason, message string) {
		result = ctrl.Result{}
		err = e
		provisionStatus = string(metav1.ConditionFalse)
		provisionStatusReason = reason
		if len(provisionStatusMessage) > 0 {
			provisionStatusMessage = fmt.Sprintf("%s: %s", message, provisionStatusMessage)
		} else {
			provisionStatusMessage = message
		}
	}

	returnNotReady := func(reason, message string) {
		result = ctrl.Result{}
		err = nil
		provisionStatus = string(metav1.ConditionFalse)
		provisionStatusReason = reason
		provisionStatusMessage = message
	}

	returnRequeue := func(reason, message string) {
		result = ctrl.Result{Requeue: true}
		err = nil
		provisionStatus = string(metav1.ConditionFalse)
		provisionStatusReason = reason
		provisionStatusMessage = message
	}

	returnReady := func() {
		result = ctrl.Result{}
		err = nil
		provisionStatus = string(metav1.ConditionTrue)
		provisionStatusReason = instanceStatusReasonReady
	}

	updateInstanceReadyCondition := func() {
		condition := metav1.Condition{
			Type:    instanceConditionReady,
			Status:  metav1.ConditionStatus(provisionStatus),
			Reason:  provisionStatusReason,
			Message: provisionStatusMessage,
		}
		apimeta.SetStatusCondition(&instance.Status.Conditions, condition)
		if len(phase) > 0 {
			instance.Status.Phase = phase
		}
		if e := r.Status().Update(ctx, &instance); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Instance modified, retry reconciling")
				result = ctrl.Result{Requeue: true}
			} else {
				logger.Error(e, "Failed to update Instance status")
				if err == nil {
					err = e
				}
			}
		}
	}

	checkFinalizer := func() bool {
		if instance.ObjectMeta.DeletionTimestamp.IsZero() {
			if !controllerutil.ContainsFinalizer(&instance, instanceFinalizer) {
				phase = instancePhasePending
				controllerutil.AddFinalizer(&instance, instanceFinalizer)
				if e := r.Update(ctx, &instance); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Instance modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to add finalizer to Instance")
					returnError(e, instanceStatusReasonBackendError, instanceStatusMessageUpdateError)
					return true
				}
				logger.Info("Finalizer added to Instance")
				returnNotReady(instanceStatusReasonUpdating, instanceStatusMessageUpdating)
				return true
			}
		} else {
			if controllerutil.ContainsFinalizer(&instance, instanceFinalizer) {
				phase = instancePhaseDeleting
				dbInstance := &rdsv1alpha1.DBInstance{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: inventory.Namespace, Name: instance.Name}, dbInstance); e != nil {
					if !errors.IsNotFound(e) {
						logger.Error(e, "Failed to get DB Instance status")
						returnError(e, instanceStatusReasonBackendError, instanceStatusMessageGetError)
						return true
					}
				} else {
					if e := r.Delete(ctx, dbInstance); e != nil {
						logger.Error(e, "Failed to delete DB Instance")
						returnError(e, instanceStatusReasonBackendError, instanceStatusMessageDeleteError)
						return true
					}
					returnUpdating()
					return true
				}

				controllerutil.RemoveFinalizer(&instance, instanceFinalizer)
				if e := r.Update(ctx, &instance); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Instance modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to remove finalizer from Instance")
					returnError(e, instanceStatusReasonBackendError, instanceStatusMessageUpdateError)
					return true
				}
				phase = instancePhaseDeleted
				returnNotReady(instanceStatusReasonUpdating, instanceStatusMessageUpdating)
				return true
			}

			// Stop reconciliation as the item is being deleted
			returnNotReady(instanceStatusReasonDeleting, instanceStatusMessageDeleting)
			return true
		}

		return false
	}

	createOrUpdateDBInstance := func() bool {
		dbInstance := &rdsv1alpha1.DBInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instance.Name,
				Namespace: inventory.Namespace,
			},
		}

		if r, e := controllerutil.CreateOrUpdate(ctx, r.Client, dbInstance, func() error {
			if e := ophandler.SetOwnerAnnotations(&instance, dbInstance); e != nil {
				logger.Error(e, "Failed to set owner for DB Instance")
				returnError(e, instanceStatusReasonBackendError, e.Error())
				return e
			}
			if e := r.setDBInstanceSpec(ctx, dbInstance, &instance); e != nil {
				logger.Error(e, "Failed to set spec for DB Instance")
				returnError(e, instanceStatusReasonInputError, e.Error())
				return e
			}
			return nil
		}); e != nil {
			logger.Error(e, "Failed to create or update DB Instance")
			returnError(e, instanceStatusReasonBackendError, instanceStatusMessageCreateOrUpdateError)
			return true
		} else if r == controllerutil.OperationResultCreated {
			phase = instancePhaseCreating
			returnRequeue(instanceStatusReasonCreating, instanceStatusMessageCreating)
			return true
		} else if r == controllerutil.OperationResultUpdated {
			phase = instancePhaseUpdating
		}
		return false
	}

	syncDBInstanceStatus := func() bool {
		dbInstance := &rdsv1alpha1.DBInstance{}
		if e := r.Get(ctx, client.ObjectKey{Namespace: inventory.Namespace, Name: instance.Name}, dbInstance); e != nil {
			logger.Error(e, "Failed to get DB Instance status")
			if errors.IsNotFound(e) {
				returnError(e, instanceStatusReasonNotFound, instanceStatusMessageGetError)
			} else {
				returnError(e, instanceStatusReasonBackendError, instanceStatusMessageGetError)
			}
			return true
		}

		instance.Status.InstanceID = *dbInstance.Spec.DBInstanceIdentifier
		setDBInstancePhase(dbInstance, &instance)
		setDBInstanceStatus(dbInstance, &instance)
		for _, condition := range dbInstance.Status.Conditions {
			c := metav1.Condition{
				Type:   string(condition.Type),
				Status: metav1.ConditionStatus(condition.Status),
			}
			if condition.LastTransitionTime != nil {
				c.LastTransitionTime = metav1.Time{Time: condition.LastTransitionTime.Time}
			}
			if condition.Reason != nil && len(*condition.Reason) > 0 {
				if match, _ := regexp.MatchString("^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$", *condition.Reason); match {
					c.Reason = *condition.Reason
					if condition.Message != nil {
						c.Message = *condition.Message
					}
				} else {
					c.Reason = instanceStatusReasonDBInstance
					if condition.Message != nil {
						c.Message = fmt.Sprintf("Reason: %s, Message: %s", *condition.Reason, *condition.Message)
					} else {
						c.Message = fmt.Sprintf("Reason: %s", *condition.Reason)
					}
				}
			} else {
				c.Reason = instanceStatusReasonDBInstance
				if condition.Message != nil {
					c.Message = *condition.Message
				}
			}
			apimeta.SetStatusCondition(&instance.Status.Conditions, c)
		}

		if e := r.Status().Update(ctx, &instance); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Instance modified, retry reconciling")
				returnUpdating()
				return true
			}
			logger.Error(e, "Failed to sync Instance status")
			returnError(e, instanceStatusReasonBackendError, instanceStatusMessageUpdateError)
			return true
		}
		return false
	}

	if err = r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Instance resource not found, has been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get RDS Instance")
		return ctrl.Result{}, err
	}

	defer updateInstanceReadyCondition()

	if e := r.Get(ctx, client.ObjectKey{Namespace: instance.Spec.InventoryRef.Namespace,
		Name: instance.Spec.InventoryRef.Name}, &inventory); e != nil {
		if errors.IsNotFound(e) {
			logger.Info("RDS Inventory resource not found, may have been deleted")
			returnError(e, instanceStatusReasonNotFound, instanceStatusMessageInventoryNotFound)
			return
		}
		logger.Error(e, "Failed to get RDS Inventory")
		returnError(e, instanceStatusReasonBackendError, instanceStatusMessageGetInventoryError)
		return
	}

	if condition := apimeta.FindStatusCondition(inventory.Status.Conditions, inventoryConditionReady); condition == nil ||
		condition.Status != metav1.ConditionTrue {
		logger.Info("RDS Inventory not ready")
		returnRequeue(instanceStatusReasonUnreachable, instanceStatusMessageInventoryNotReady)
		return
	}

	if checkFinalizer() {
		return
	}

	if createOrUpdateDBInstance() {
		return
	}

	if syncDBInstanceStatus() {
		return
	}

	switch instance.Status.Phase {
	case instancePhaseReady:
		returnReady()
	case instancePhaseFailed, instancePhaseDeleted:
		returnNotReady(instanceStatusReasonTerminated, instance.Status.Phase)
	case instancePhasePending, instancePhaseCreating, instancePhaseUpdating, instancePhaseDeleting:
		returnUpdating()
	case instancePhaseError, instancePhaseUnknown:
		returnRequeue(instanceStatusReasonBackendError, instanceStatusMessageError)
	default:
	}

	return
}

func (r *RDSInstanceReconciler) setDBInstanceSpec(ctx context.Context, dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) error {
	if engine, ok := rdsInstance.Spec.OtherInstanceParams[engine]; ok {
		dbInstance.Spec.Engine = &engine
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "Engine")
	}

	if engineVersion, ok := rdsInstance.Spec.OtherInstanceParams[engineVersion]; ok {
		dbInstance.Spec.EngineVersion = &engineVersion
	}

	if dbInstanceId, ok := rdsInstance.Spec.OtherInstanceParams[dbInstanceIdentifier]; ok {
		dbInstance.Spec.DBInstanceIdentifier = &dbInstanceId
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "DBInstanceIdentifier")
	}

	if dbInstanceClass, ok := rdsInstance.Spec.OtherInstanceParams[dbInstanceClass]; ok {
		dbInstance.Spec.DBInstanceClass = &dbInstanceClass
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "DBInstanceClass")
	}

	if storageType, ok := rdsInstance.Spec.OtherInstanceParams[storageType]; ok {
		dbInstance.Spec.StorageType = &storageType
	}

	if allocatedStorage, ok := rdsInstance.Spec.OtherInstanceParams[allocatedStorage]; ok {
		if i, e := strconv.ParseInt(allocatedStorage, 10, 64); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "AllocatedStorage")
		} else {
			dbInstance.Spec.AllocatedStorage = &i
		}
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "AllocatedStorage")
	}

	if iops, ok := rdsInstance.Spec.OtherInstanceParams[iops]; ok {
		if i, e := strconv.ParseInt(iops, 10, 64); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "IOPS")
		} else {
			dbInstance.Spec.IOPS = &i
		}
	}

	if maxAllocatedStorage, ok := rdsInstance.Spec.OtherInstanceParams[maxAllocatedStorage]; ok {
		if i, e := strconv.ParseInt(maxAllocatedStorage, 10, 64); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "MaxAllocatedStorage")
		} else {
			dbInstance.Spec.MaxAllocatedStorage = &i
		}
	}

	if dbSubnetGroupName, ok := rdsInstance.Spec.OtherInstanceParams[dbSubnetGroupName]; ok {
		dbInstance.Spec.DBSubnetGroupName = &dbSubnetGroupName
	}

	if publiclyAccessible, ok := rdsInstance.Spec.OtherInstanceParams[publiclyAccessible]; ok {
		if b, e := strconv.ParseBool(publiclyAccessible); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "PubliclyAccessible")
		} else {
			dbInstance.Spec.PubliclyAccessible = &b
		}
	}

	if vpcSecurityGroupIDs, ok := rdsInstance.Spec.OtherInstanceParams[vpcSecurityGroupIDs]; ok {
		sl := strings.Split(vpcSecurityGroupIDs, ",")
		var sgs []*string
		for _, s := range sl {
			st := s
			sgs = append(sgs, &st)
		}
		dbInstance.Spec.VPCSecurityGroupIDs = sgs
	}

	if e := setCredentials(ctx, r.Client, r.Scheme, dbInstance, rdsInstance.Namespace, rdsInstance, rdsInstance.Kind); e != nil {
		return fmt.Errorf("failed to set credentials for DB instance")
	}

	dbName := generateDBName(*dbInstance.Spec.Engine)
	dbInstance.Spec.DBName = dbName

	dbInstance.Spec.AvailabilityZone = &rdsInstance.Spec.CloudRegion

	return nil
}

func setCredentials(ctx context.Context, cli client.Client, scheme *runtime.Scheme, dbInstance *rdsv1alpha1.DBInstance,
	namespace string, owner metav1.Object, kind string) error {
	logger := log.FromContext(ctx)

	secretName := fmt.Sprintf("%s-credentials", dbInstance.GetName())
	secret := &v1.Secret{}
	if e := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); e != nil {
		if errors.IsNotFound(e) {
			secret.Name = secretName
			secret.Namespace = namespace
			secret.ObjectMeta.Labels = createSecretLabels(owner, kind)
			if owner != nil {
				if e := ctrl.SetControllerReference(owner, secret, scheme); e != nil {
					logger.Error(e, "Failed to set owner reference for credential Secret")
					return e
				}
			}
			secret.Data = map[string][]byte{
				"username": []byte(generateUsername(*dbInstance.Spec.Engine)),
				"password": []byte(generatePassword()),
			}
			if e := cli.Create(ctx, secret); e != nil {
				logger.Error(e, "Failed to create credential secret")
				return e
			}
		} else {
			logger.Error(e, "Failed to retrieve credential secret")
			return e
		}
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}

	var username string
	u, nok := secret.Data["username"]
	if !nok {
		username = generateUsername(*dbInstance.Spec.Engine)
		secret.Data["username"] = []byte(username)
	} else {
		username = string(u)
	}

	_, pok := secret.Data["password"]
	if !pok {
		secret.Data["password"] = []byte(generatePassword())
	}

	if !nok || !pok {
		if e := cli.Update(ctx, secret); e != nil {
			logger.Error(e, "Failed to update credential secret")
			return e
		}
	}

	dbInstance.Spec.MasterUsername = &username

	dbInstance.Spec.MasterUserPassword = &ackv1alpha1.SecretKeyReference{
		SecretReference: v1.SecretReference{
			Name:      secretName,
			Namespace: namespace,
		},
		Key: "password",
	}

	return nil
}

func createSecretLabels(owner metav1.Object, kind string) map[string]string {
	if owner != nil {
		return map[string]string{
			"managed-by":               "rds-dbaas-operator",
			"owner":                    owner.GetName(),
			"owner.kind":               kind,
			"owner.namespace":          owner.GetNamespace(),
			dbaasv1alpha1.TypeLabelKey: dbaasv1alpha1.TypeLabelValue,
		}
	} else {
		return map[string]string{
			dbaasv1alpha1.TypeLabelKey: dbaasv1alpha1.TypeLabelValue,
		}
	}
}

func setDBInstancePhase(dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) {
	var status string
	if dbInstance.Status.DBInstanceStatus != nil {
		status = *dbInstance.Status.DBInstanceStatus
	} else {
		status = ""
	}
	switch status {
	case "available":
		rdsInstance.Status.Phase = instancePhaseReady
	case "creating":
		rdsInstance.Status.Phase = instancePhaseCreating
	case "deleting":
		rdsInstance.Status.Phase = instancePhaseDeleting
	case "failed":
		rdsInstance.Status.Phase = instancePhaseFailed
	case "inaccessible-encryption-credentials-recoverable", "incompatible-parameters", "restore-error":
		rdsInstance.Status.Phase = instancePhaseError
	case "backing-up", "configuring-enhanced-monitoring", "configuring-iam-database-auth", "configuring-log-exports",
		"converting-to-vpc", "maintenance", "modifying", "moving-to-vpc", "rebooting", "resetting-master-credentials",
		"renaming", "starting", "stopping", "storage-optimization", "upgrading":
		rdsInstance.Status.Phase = instancePhaseUpdating
	case "inaccessible-encryption-credentials", "incompatible-network", "incompatible-option-group", "incompatible-restore",
		"insufficient-capacity", "stopped", "storage-full":
		rdsInstance.Status.Phase = instancePhaseUnknown
	default:
		rdsInstance.Status.Phase = instancePhaseUnknown
	}
}

func setDBInstanceStatus(dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) {
	instanceStatus := parseDBInstanceStatus(dbInstance)
	rdsInstance.Status.InstanceInfo = instanceStatus
}

func parseDBInstanceStatus(dbInstance *rdsv1alpha1.DBInstance) map[string]string {
	instanceStatus := map[string]string{}
	if dbInstance.Status.ACKResourceMetadata != nil {
		if dbInstance.Status.ACKResourceMetadata.ARN != nil {
			instanceStatus["ackResourceMetadata.arn"] = string(*dbInstance.Status.ACKResourceMetadata.ARN)
		}
		if dbInstance.Status.ACKResourceMetadata.OwnerAccountID != nil {
			instanceStatus["ackResourceMetadata.ownerAccountID"] = string(*dbInstance.Status.ACKResourceMetadata.OwnerAccountID)
		}
		if dbInstance.Status.ACKResourceMetadata.Region != nil {
			instanceStatus["ackResourceMetadata.region"] = string(*dbInstance.Status.ACKResourceMetadata.Region)
		}
	}
	if dbInstance.Status.ActivityStreamEngineNativeAuditFieldsIncluded != nil {
		instanceStatus["activityStreamEngineNativeAuditFieldsIncluded"] = strconv.FormatBool(*dbInstance.Status.ActivityStreamEngineNativeAuditFieldsIncluded)
	}
	if dbInstance.Status.ActivityStreamKinesisStreamName != nil {
		instanceStatus["activityStreamKinesisStreamName"] = *dbInstance.Status.ActivityStreamKinesisStreamName
	}
	if dbInstance.Status.ActivityStreamKMSKeyID != nil {
		instanceStatus["activityStreamKMSKeyID"] = *dbInstance.Status.ActivityStreamKMSKeyID
	}
	if dbInstance.Status.ActivityStreamMode != nil {
		instanceStatus["activityStreamMode"] = *dbInstance.Status.ActivityStreamMode
	}
	if dbInstance.Status.ActivityStreamStatus != nil {
		instanceStatus["activityStreamStatus"] = *dbInstance.Status.ActivityStreamStatus
	}
	if dbInstance.Status.AssociatedRoles != nil {
		for i, r := range dbInstance.Status.AssociatedRoles {
			if r.FeatureName != nil {
				instanceStatus[fmt.Sprintf("associatedRoles[%d].featureName", i)] = *r.FeatureName
			}
			if r.RoleARN != nil {
				instanceStatus[fmt.Sprintf("associatedRoles[%d].roleARN", i)] = *r.RoleARN
			}
			if r.Status != nil {
				instanceStatus[fmt.Sprintf("associatedRoles[%d].status", i)] = *r.Status
			}
		}
	}
	if dbInstance.Status.AutomaticRestartTime != nil {
		instanceStatus["automaticRestartTime"] = dbInstance.Status.AutomaticRestartTime.String()
	}
	if dbInstance.Status.AutomationMode != nil {
		instanceStatus["automationMode"] = *dbInstance.Status.AutomationMode
	}
	if dbInstance.Status.AWSBackupRecoveryPointARN != nil {
		instanceStatus["awsBackupRecoveryPointARN"] = *dbInstance.Status.AWSBackupRecoveryPointARN
	}
	if dbInstance.Status.CACertificateIdentifier != nil {
		instanceStatus["caCertificateIdentifier"] = *dbInstance.Status.CACertificateIdentifier
	}
	if dbInstance.Status.CustomerOwnedIPEnabled != nil {
		instanceStatus["customerOwnedIPEnabled"] = strconv.FormatBool(*dbInstance.Status.CustomerOwnedIPEnabled)
	}
	if dbInstance.Status.DBInstanceAutomatedBackupsReplications != nil {
		for i, r := range dbInstance.Status.DBInstanceAutomatedBackupsReplications {
			if r.DBInstanceAutomatedBackupsARN != nil {
				instanceStatus[fmt.Sprintf("dbInstanceAutomatedBackupsReplications[%d].dbInstanceAutomatedBackupsARN", i)] = *r.DBInstanceAutomatedBackupsARN
			}
		}
	}
	if dbInstance.Status.DBInstanceStatus != nil {
		instanceStatus["dbInstanceStatus"] = *dbInstance.Status.DBInstanceStatus
	}
	if dbInstance.Status.DBParameterGroups != nil {
		for i, g := range dbInstance.Status.DBParameterGroups {
			if g.DBParameterGroupName != nil {
				instanceStatus[fmt.Sprintf("dbParameterGroups[%d].dbParameterGroupName", i)] = *g.DBParameterGroupName
			}
			if g.ParameterApplyStatus != nil {
				instanceStatus[fmt.Sprintf("dbParameterGroups[%d].parameterApplyStatus", i)] = *g.ParameterApplyStatus
			}
		}
	}
	if dbInstance.Status.DBSubnetGroup != nil {
		if dbInstance.Status.DBSubnetGroup.DBSubnetGroupARN != nil {
			instanceStatus["dbSubnetGroup.dbSubnetGroupARN"] = *dbInstance.Status.DBSubnetGroup.DBSubnetGroupARN
		}
		if dbInstance.Status.DBSubnetGroup.DBSubnetGroupDescription != nil {
			instanceStatus["dbSubnetGroup.dbSubnetGroupDescription"] = *dbInstance.Status.DBSubnetGroup.DBSubnetGroupDescription
		}
		if dbInstance.Status.DBSubnetGroup.DBSubnetGroupName != nil {
			instanceStatus["dbSubnetGroup.dbSubnetGroupName"] = *dbInstance.Status.DBSubnetGroup.DBSubnetGroupName
		}
		if dbInstance.Status.DBSubnetGroup.SubnetGroupStatus != nil {
			instanceStatus["dbSubnetGroup.subnetGroupStatus"] = *dbInstance.Status.DBSubnetGroup.SubnetGroupStatus
		}
		if dbInstance.Status.DBSubnetGroup.Subnets != nil {
			for i, s := range dbInstance.Status.DBSubnetGroup.Subnets {
				if s.SubnetAvailabilityZone != nil {
					if s.SubnetAvailabilityZone.Name != nil {
						instanceStatus[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetAvailabilityZone.name", i)] = *s.SubnetAvailabilityZone.Name
					}
				}
				if s.SubnetIdentifier != nil {
					instanceStatus[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetIdentifier", i)] = *s.SubnetIdentifier
				}
				if s.SubnetOutpost != nil {
					if s.SubnetOutpost.ARN != nil {
						instanceStatus[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetOutpost.arn", i)] = *s.SubnetOutpost.ARN
					}
				}
				if s.SubnetStatus != nil {
					instanceStatus[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetStatus", i)] = *s.SubnetStatus
				}
			}
		}
		if dbInstance.Status.DBSubnetGroup.VPCID != nil {
			instanceStatus["dbSubnetGroup.vpcID"] = *dbInstance.Status.DBSubnetGroup.VPCID
		}
	}
	if dbInstance.Status.DBInstancePort != nil {
		instanceStatus["dbInstancePort"] = strconv.FormatInt(*dbInstance.Status.DBInstancePort, 10)
	}
	if dbInstance.Status.DBIResourceID != nil {
		instanceStatus["dbiResourceID"] = *dbInstance.Status.DBIResourceID
	}
	if dbInstance.Status.DomainMemberships != nil {
		for i, m := range dbInstance.Status.DomainMemberships {
			if m.Domain != nil {
				instanceStatus[fmt.Sprintf("domainMemberships[%d].domain", i)] = *m.Domain
			}
			if m.FQDN != nil {
				instanceStatus[fmt.Sprintf("domainMemberships[%d].fQDN", i)] = *m.FQDN
			}
			if m.IAMRoleName != nil {
				instanceStatus[fmt.Sprintf("domainMemberships[%d].iamRoleName", i)] = *m.IAMRoleName
			}
			if m.Status != nil {
				instanceStatus[fmt.Sprintf("domainMemberships[%d].status", i)] = *m.Status
			}
		}
	}
	if dbInstance.Status.EnabledCloudwatchLogsExports != nil {
		for i, e := range dbInstance.Status.EnabledCloudwatchLogsExports {
			if e != nil {
				instanceStatus[fmt.Sprintf("enabledCloudwatchLogsExports[%d]", i)] = *e
			}
		}
	}
	if dbInstance.Status.Endpoint != nil {
		if dbInstance.Status.Endpoint.Address != nil {
			instanceStatus["endpoint.address"] = *dbInstance.Status.Endpoint.Address
		}
		if dbInstance.Status.Endpoint.HostedZoneID != nil {
			instanceStatus["endpoint.hostedZoneID"] = *dbInstance.Status.Endpoint.HostedZoneID
		}
		if dbInstance.Status.Endpoint.Port != nil {
			instanceStatus["endpoint.port"] = strconv.FormatInt(*dbInstance.Status.Endpoint.Port, 10)
		}
	}
	if dbInstance.Status.EnhancedMonitoringResourceARN != nil {
		instanceStatus["enhancedMonitoringResourceARN"] = *dbInstance.Status.EnhancedMonitoringResourceARN
	}
	if dbInstance.Status.IAMDatabaseAuthenticationEnabled != nil {
		instanceStatus["iamDatabaseAuthenticationEnabled"] = strconv.FormatBool(*dbInstance.Status.IAMDatabaseAuthenticationEnabled)
	}
	if dbInstance.Status.InstanceCreateTime != nil {
		instanceStatus["instanceCreateTime"] = dbInstance.Status.InstanceCreateTime.String()
	}
	if dbInstance.Status.LatestRestorableTime != nil {
		instanceStatus["latestRestorableTime"] = dbInstance.Status.LatestRestorableTime.String()
	}
	if dbInstance.Status.ListenerEndpoint != nil {
		if dbInstance.Status.ListenerEndpoint.Address != nil {
			instanceStatus["listenerEndpoint.address"] = *dbInstance.Status.ListenerEndpoint.Address
		}
		if dbInstance.Status.ListenerEndpoint.HostedZoneID != nil {
			instanceStatus["listenerEndpoint.hostedZoneID"] = *dbInstance.Status.ListenerEndpoint.HostedZoneID
		}
		if dbInstance.Status.ListenerEndpoint.Port != nil {
			instanceStatus["listenerEndpoint.port"] = strconv.FormatInt(*dbInstance.Status.ListenerEndpoint.Port, 10)
		}
	}
	if dbInstance.Status.OptionGroupMemberships != nil {
		for i, m := range dbInstance.Status.OptionGroupMemberships {
			if m.OptionGroupName != nil {
				instanceStatus[fmt.Sprintf("optionGroupMemberships[%d].optionGroupName", i)] = *m.OptionGroupName
			}
			if m.Status != nil {
				instanceStatus[fmt.Sprintf("optionGroupMemberships[%d].status", i)] = *m.Status
			}
		}
	}
	if dbInstance.Status.PendingModifiedValues != nil {
		if dbInstance.Status.PendingModifiedValues.AllocatedStorage != nil {
			instanceStatus["pendingModifiedValues.allocatedStorage"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.AllocatedStorage, 10)
		}
		if dbInstance.Status.PendingModifiedValues.AutomationMode != nil {
			instanceStatus["pendingModifiedValues.automationMode"] = *dbInstance.Status.PendingModifiedValues.AutomationMode
		}
		if dbInstance.Status.PendingModifiedValues.BackupRetentionPeriod != nil {
			instanceStatus["pendingModifiedValues.backupRetentionPeriod"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.BackupRetentionPeriod, 10)
		}
		if dbInstance.Status.PendingModifiedValues.CACertificateIdentifier != nil {
			instanceStatus["pendingModifiedValues.caCertificateIdentifier"] = *dbInstance.Status.PendingModifiedValues.CACertificateIdentifier
		}
		if dbInstance.Status.PendingModifiedValues.DBInstanceClass != nil {
			instanceStatus["pendingModifiedValues.dbInstanceClass"] = *dbInstance.Status.PendingModifiedValues.DBInstanceClass
		}
		if dbInstance.Status.PendingModifiedValues.DBInstanceIdentifier != nil {
			instanceStatus["pendingModifiedValues.dbInstanceIdentifier"] = *dbInstance.Status.PendingModifiedValues.DBInstanceIdentifier
		}
		if dbInstance.Status.PendingModifiedValues.DBSubnetGroupName != nil {
			instanceStatus["pendingModifiedValues.dbSubnetGroupName"] = *dbInstance.Status.PendingModifiedValues.DBSubnetGroupName
		}
		if dbInstance.Status.PendingModifiedValues.EngineVersion != nil {
			instanceStatus["pendingModifiedValues.engineVersion"] = *dbInstance.Status.PendingModifiedValues.EngineVersion
		}
		if dbInstance.Status.PendingModifiedValues.IAMDatabaseAuthenticationEnabled != nil {
			instanceStatus["pendingModifiedValues.iamDatabaseAuthenticationEnabled"] = strconv.FormatBool(*dbInstance.Status.PendingModifiedValues.IAMDatabaseAuthenticationEnabled)
		}
		if dbInstance.Status.PendingModifiedValues.IOPS != nil {
			instanceStatus["pendingModifiedValues.iops"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.IOPS, 10)
		}
		if dbInstance.Status.PendingModifiedValues.LicenseModel != nil {
			instanceStatus["pendingModifiedValues.licenseModel"] = *dbInstance.Status.PendingModifiedValues.LicenseModel
		}
		if dbInstance.Status.PendingModifiedValues.MasterUserPassword != nil {
			instanceStatus["pendingModifiedValues.masterUserPassword"] = *dbInstance.Status.PendingModifiedValues.MasterUserPassword
		}
		if dbInstance.Status.PendingModifiedValues.MultiAZ != nil {
			instanceStatus["pendingModifiedValues.multiAZ"] = strconv.FormatBool(*dbInstance.Status.PendingModifiedValues.MultiAZ)
		}
		if dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports != nil {
			if dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToDisable != nil {
				for i, d := range dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToDisable {
					if d != nil {
						instanceStatus[fmt.Sprintf("pendingModifiedValues.pendingCloudwatchLogsExports.logTypesToDisable[%d]", i)] = *d
					}
				}
			}
			if dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToEnable != nil {
				for i, e := range dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToEnable {
					if e != nil {
						instanceStatus[fmt.Sprintf("pendingModifiedValues.pendingCloudwatchLogsExports.logTypesToEnable[%d]", i)] = *e
					}
				}
			}
		}
		if dbInstance.Status.PendingModifiedValues.Port != nil {
			instanceStatus["pendingModifiedValues.port"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.Port, 10)
		}
		if dbInstance.Status.PendingModifiedValues.ProcessorFeatures != nil {
			for i, f := range dbInstance.Status.PendingModifiedValues.ProcessorFeatures {
				if f.Name != nil {
					instanceStatus[fmt.Sprintf("pendingModifiedValues.ProcessorFeature[%d].name", i)] = *f.Name
				}
				if f.Value != nil {
					instanceStatus[fmt.Sprintf("pendingModifiedValues.ProcessorFeature[%d].value", i)] = *f.Value
				}
			}
		}
		if dbInstance.Status.PendingModifiedValues.ResumeFullAutomationModeTime != nil {
			instanceStatus["pendingModifiedValues.resumeFullAutomationModeTime"] = dbInstance.Status.PendingModifiedValues.ResumeFullAutomationModeTime.String()
		}
		if dbInstance.Status.PendingModifiedValues.StorageType != nil {
			instanceStatus["pendingModifiedValues.storageType"] = *dbInstance.Status.PendingModifiedValues.StorageType
		}
	}
	if dbInstance.Status.PerformanceInsightsEnabled != nil {
		instanceStatus["performanceInsightsEnabled"] = strconv.FormatBool(*dbInstance.Status.PerformanceInsightsEnabled)
	}
	if dbInstance.Status.ReadReplicaDBClusterIdentifiers != nil {
		for i, id := range dbInstance.Status.ReadReplicaDBClusterIdentifiers {
			if id != nil {
				instanceStatus[fmt.Sprintf("readReplicaDBClusterIdentifiers[%d]", i)] = *id
			}
		}
	}
	if dbInstance.Status.ReadReplicaDBInstanceIdentifiers != nil {
		for i, id := range dbInstance.Status.ReadReplicaDBInstanceIdentifiers {
			if id != nil {
				instanceStatus[fmt.Sprintf("readReplicaDBInstanceIdentifiers[%d]", i)] = *id
			}
		}
	}
	if dbInstance.Status.ReadReplicaSourceDBInstanceIdentifier != nil {
		instanceStatus["readReplicaSourceDBInstanceIdentifier"] = *dbInstance.Status.ReadReplicaSourceDBInstanceIdentifier
	}
	if dbInstance.Status.ReplicaMode != nil {
		instanceStatus["replicaMode"] = *dbInstance.Status.ReplicaMode
	}
	if dbInstance.Status.ResumeFullAutomationModeTime != nil {
		instanceStatus["resumeFullAutomationModeTime"] = dbInstance.Status.ResumeFullAutomationModeTime.String()
	}
	if dbInstance.Status.SecondaryAvailabilityZone != nil {
		instanceStatus["secondaryAvailabilityZone"] = *dbInstance.Status.SecondaryAvailabilityZone
	}
	if dbInstance.Status.StatusInfos != nil {
		for i, info := range dbInstance.Status.StatusInfos {
			if info.Message != nil {
				instanceStatus[fmt.Sprintf("statusInfos[%d].message", i)] = *info.Message
			}
			if info.Normal != nil {
				instanceStatus[fmt.Sprintf("statusInfos[%d].normal", i)] = strconv.FormatBool(*info.Normal)
			}
			if info.Status != nil {
				instanceStatus[fmt.Sprintf("statusInfos[%d].status", i)] = *info.Status
			}
			if info.StatusType != nil {
				instanceStatus[fmt.Sprintf("statusInfos[%d].statusType", i)] = *info.StatusType
			}
		}
	}
	if dbInstance.Status.TagList != nil {
		for i, l := range dbInstance.Status.TagList {
			if l.Key != nil {
				instanceStatus[fmt.Sprintf("tagList[%d].key", i)] = *l.Key
			}
			if l.Value != nil {
				instanceStatus[fmt.Sprintf("tagList[%d].value", i)] = *l.Value
			}
		}
	}
	if dbInstance.Status.VPCSecurityGroups != nil {
		for i, g := range dbInstance.Status.VPCSecurityGroups {
			if g.Status != nil {
				instanceStatus[fmt.Sprintf("vpcSecurityGroups[%d].status", i)] = *g.Status
			}
			if g.VPCSecurityGroupID != nil {
				instanceStatus[fmt.Sprintf("vpcSecurityGroups[%d].vpcSecurityGroupID", i)] = *g.VPCSecurityGroupID
			}
		}
	}
	return instanceStatus
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rdsdbaasv1alpha1.RDSInstance{}).
		Watches(
			&source.Kind{Type: &rdsv1alpha1.DBInstance{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				return []reconcile.Request{getOwnerInstanceRequests(o)}
			}),
		).
		Complete(r)
}

// Code from operator-lib: https://github.com/operator-framework/operator-lib/blob/d389ad4d93a46dba047b11161b755141fc853098/handler/enqueue_annotation.go#L121
func getOwnerInstanceRequests(object client.Object) reconcile.Request {
	if typeString, ok := object.GetAnnotations()[ophandler.TypeAnnotation]; ok && typeString == rdsInstanceType {
		namespacedNameString, ok := object.GetAnnotations()[ophandler.NamespacedNameAnnotation]
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
