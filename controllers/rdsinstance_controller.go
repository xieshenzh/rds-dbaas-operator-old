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
	"encoding/base64"
	"fmt"
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
	instanceFinalizer = "rds.dbaas.redhat.com/instance"

	instanceConditionReady = "ProvisionReady"

	statusReasonUpdating       = "Updating"
	statusReasonDeleting       = "Deleting"
	statusReasonTerminated     = "Terminated"
	statusReasonError          = "Error"
	statusReasonInventoryError = "Inventory error"

	statusMessageUpdateError         = "Failed to update Instance"
	statusMessageUpdating            = "Updating Instance"
	statusMessageDeleting            = "Deleting Instance"
	statusMessageError               = "Instance with error"
	statusMessageCreateOrUpdateError = "Failed to create or update DB Instance"
	statusMessageInventoryNotFound   = "Inventory not Found"
	statusMessageInventoryNotReady   = "Inventory not ready"
	statusMessageGetInventoryError   = "Failed to get Inventory"
	statusMessageGetInstanceError    = "Failed to get DB Instance"

	phasePending  = "Pending"
	phaseCreating = "Creating"
	phaseUpdating = "Updating"
	phaseDeleting = "Deleting"
	phaseDeleted  = "Deleted"
	phaseReady    = "Ready"
	phaseError    = "Error"
	phaseFailed   = "Failed"
	phaseUnknown  = "Unknown"

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
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch;create;update;patch;delete

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
		provisionStatusReason = statusReasonUpdating
		provisionStatusMessage = statusMessageUpdating
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
		provisionStatus = string(metav1.ConditionTrue)
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
				phase = phasePending
				controllerutil.AddFinalizer(&instance, instanceFinalizer)
				if e := r.Update(ctx, &instance); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Instance modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to add finalizer to Instance")
					returnError(e, statusReasonError, statusMessageUpdateError)
					return true
				}
				logger.Info("Finalizer added to Instance")
				returnNotReady(statusReasonUpdating, statusMessageUpdating)
				return true
			}
		} else {
			if controllerutil.ContainsFinalizer(&instance, instanceFinalizer) {
				phase = phaseDeleting
				//TODO delete rds db instance

				controllerutil.RemoveFinalizer(&instance, instanceFinalizer)
				if e := r.Update(ctx, &instance); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Instance modified, retry reconciling")
						returnUpdating()
						return true
					}
					logger.Error(e, "Failed to remove finalizer from Instance")
					returnError(e, statusReasonError, statusMessageUpdateError)
					return true
				}
				phase = phaseDeleted
				returnNotReady(statusReasonUpdating, statusMessageUpdating)
				return true
			}

			// Stop reconciliation as the item is being deleted
			returnNotReady(statusReasonDeleting, statusMessageDeleting)
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
				logger.Error(e, "Failed to set owner for DBInstance")
				returnError(e, statusReasonError, e.Error())
				return e
			}
			if e := ctrl.SetControllerReference(&inventory, dbInstance, r.Scheme); e != nil {
				logger.Error(e, "Failed to set owner reference for the DBInstance")
				returnError(e, statusReasonError, e.Error())
				return e
			}
			if e := r.setDBInstanceSpec(ctx, dbInstance, &instance); e != nil {
				logger.Error(e, "Failed to set spec for DBInstance")
				returnError(e, statusReasonError, e.Error())
				return e
			}
			return nil
		}); e != nil {
			logger.Error(e, "Failed to create or update the DBInstance")
			returnError(e, statusReasonError, statusMessageCreateOrUpdateError)
			return true
		} else if r == controllerutil.OperationResultCreated {
			phase = phaseCreating
		} else if r == controllerutil.OperationResultUpdated {
			phase = phaseUpdating
		}

		return false
	}

	syncDBInstanceStatus := func() bool {
		dbInstance := &rdsv1alpha1.DBInstance{}
		if e := r.Get(ctx, client.ObjectKey{Namespace: inventory.Namespace, Name: instance.Name}, dbInstance); e != nil {
			logger.Error(e, "Failed to get DBInstance status")
			returnError(e, statusReasonError, statusMessageGetInstanceError)
			return true
		}

		instance.Status.InstanceID = *dbInstance.Spec.DBInstanceIdentifier
		setDBInstancePhase(dbInstance, &instance)
		setDBInstanceStatus(dbInstance, &instance)
		for _, condition := range dbInstance.Status.Conditions {
			apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
				Type:               string(condition.Type),
				Status:             metav1.ConditionStatus(condition.Status),
				LastTransitionTime: metav1.Time{Time: condition.LastTransitionTime.Time},
				Reason:             *condition.Reason,
				Message:            *condition.Message,
			})
		}

		if e := r.Status().Update(ctx, &instance); e != nil {
			if errors.IsConflict(e) {
				logger.Info("Instance modified, retry reconciling")
				returnUpdating()
				return true
			}
			logger.Error(e, "Failed to sync Instance status")
			returnError(e, statusReasonError, statusMessageUpdateError)
			return true
		}
		return false
	}

	if err = r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RDS Instance resource not found, has been deleted")
			return
		}
		logger.Error(err, "Error fetching RDS Instance for reconcile")
		return
	}

	defer updateInstanceReadyCondition()

	if e := r.Get(ctx, client.ObjectKey{Namespace: instance.Spec.InventoryRef.Namespace, Name: instance.Spec.InventoryRef.Name}, &inventory); e != nil {
		if errors.IsNotFound(e) {
			logger.Info("RDS Inventory resource not found, may have been deleted")
			returnError(e, statusReasonInventoryError, statusMessageInventoryNotFound)
			return
		}
		logger.Error(e, "Error fetching RDS Inventory for reconcile")
		returnError(e, statusReasonInventoryError, statusMessageGetInventoryError)
		return
	}

	if condition := apimeta.FindStatusCondition(inventory.Status.Conditions, inventoryConditionReady); condition == nil || condition.Status != metav1.ConditionTrue {
		returnRequeue(statusReasonError, statusMessageInventoryNotReady)
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
	case phaseReady:
		returnReady()
	case phaseFailed, phaseDeleted:
		returnNotReady(statusReasonTerminated, instance.Status.Phase)
	case phasePending, phaseCreating, phaseUpdating, phaseDeleting:
		returnUpdating()
	case phaseError, phaseUnknown:
		returnRequeue(statusReasonError, statusMessageError)
	default:
	}

	return
}

func (r *RDSInstanceReconciler) setDBInstanceSpec(ctx context.Context, dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) error {
	if engine, ok := rdsInstance.Spec.OtherInstanceParams["Engine"]; ok {
		dbInstance.Spec.Engine = &engine
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "Engine")
	}

	if engineVersion, ok := rdsInstance.Spec.OtherInstanceParams["EngineVersion"]; ok {
		dbInstance.Spec.EngineVersion = &engineVersion
	}

	if dbInstanceId, ok := rdsInstance.Spec.OtherInstanceParams["DBInstanceIdentifier"]; ok {
		dbInstance.Spec.DBInstanceIdentifier = &dbInstanceId
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "DBInstanceIdentifier")
	}

	if dbInstanceClass, ok := rdsInstance.Spec.OtherInstanceParams["DBInstanceClass"]; ok {
		dbInstance.Spec.DBInstanceClass = &dbInstanceClass
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "DBInstanceClass")
	}

	if storageType, ok := rdsInstance.Spec.OtherInstanceParams["StorageType"]; ok {
		dbInstance.Spec.StorageType = &storageType
	}

	if allocatedStorage, ok := rdsInstance.Spec.OtherInstanceParams["AllocatedStorage"]; ok {
		if i, e := strconv.ParseInt(allocatedStorage, 10, 64); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "AllocatedStorage")
		} else {
			dbInstance.Spec.AllocatedStorage = &i
		}
	} else {
		return fmt.Errorf(requiredParameterErrorTemplate, "AllocatedStorage")
	}

	if iops, ok := rdsInstance.Spec.OtherInstanceParams["IOPS"]; ok {
		if i, e := strconv.ParseInt(iops, 10, 64); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "IOPS")
		} else {
			dbInstance.Spec.IOPS = &i
		}
	}

	if maxAllocatedStorage, ok := rdsInstance.Spec.OtherInstanceParams["MaxAllocatedStorage"]; ok {
		if i, e := strconv.ParseInt(maxAllocatedStorage, 10, 64); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "MaxAllocatedStorage")
		} else {
			dbInstance.Spec.MaxAllocatedStorage = &i
		}
	}

	if dbSubnetGroupName, ok := rdsInstance.Spec.OtherInstanceParams["DBSubnetGroupName"]; ok {
		dbInstance.Spec.DBSubnetGroupName = &dbSubnetGroupName
	}

	if publiclyAccessible, ok := rdsInstance.Spec.OtherInstanceParams["PubliclyAccessible"]; ok {
		if b, e := strconv.ParseBool(publiclyAccessible); e != nil {
			return fmt.Errorf(invalidParameterErrorTemplate, "PubliclyAccessible")
		} else {
			dbInstance.Spec.PubliclyAccessible = &b
		}
	}

	if vpcSecurityGroupIDs, ok := rdsInstance.Spec.OtherInstanceParams["VPCSecurityGroupIDs"]; ok {
		sl := strings.Split(vpcSecurityGroupIDs, ",")
		var sgs []*string
		for _, s := range sl {
			st := s
			sgs = append(sgs, &st)
		}
		dbInstance.Spec.VPCSecurityGroupIDs = sgs
	}

	if e := r.setCredentials(ctx, dbInstance, rdsInstance); e != nil {
		return fmt.Errorf("failed to set credentials for the DB instance")
	}

	dbName := generateDBName(*dbInstance.Spec.Engine)
	dbInstance.Spec.DBName = &dbName

	dbInstance.Spec.AvailabilityZone = &rdsInstance.Spec.CloudRegion

	return nil
}

func (r *RDSInstanceReconciler) setCredentials(ctx context.Context, dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) error {
	logger := log.FromContext(ctx)

	secretName := fmt.Sprintf("%s-credentials", rdsInstance.Name)
	secret := &v1.Secret{}
	if e := r.Get(ctx, client.ObjectKey{Namespace: rdsInstance.Namespace, Name: secretName}, secret); e != nil {
		if errors.IsNotFound(e) {
			secret.Name = secretName
			secret.Namespace = rdsInstance.Namespace
			secret.ObjectMeta.Labels = createSecretLabels(rdsInstance)
			if e := ctrl.SetControllerReference(rdsInstance, secret, r.Scheme); e != nil {
				logger.Error(e, "Failed to set owner reference for the credential secret")
				return e
			}
			secret.Data["username"] = []byte(generateUsername(*dbInstance.Spec.Engine))
			secret.Data["password"] = []byte(generatePassword())
			if e := r.Create(ctx, secret); e != nil {
				logger.Error(e, "Failed to create the credential secret")
				return e
			}
		}
		logger.Error(e, "Failed to retrieve the credential secret")
		return e
	}

	var username string
	u, nok := secret.Data["username"]
	if !nok {
		username = generateUsername(*dbInstance.Spec.Engine)
		secret.Data["username"] = []byte(username)
	} else {
		var du []byte
		if _, e := base64.StdEncoding.Decode(du, u); e != nil {
			logger.Error(e, "Failed to decode the username in the credential secret")
			return e
		}
		username = string(du)
	}

	_, pok := secret.Data["password"]
	if !pok {
		secret.Data["password"] = []byte(generatePassword())
	}

	if !nok || !pok {
		if e := r.Update(ctx, secret); e != nil {
			logger.Error(e, "Failed to update the credential secret")
			return e
		}
	}

	dbInstance.Spec.MasterUsername = &username

	dbInstance.Spec.MasterUserPassword = &ackv1alpha1.SecretKeyReference{
		SecretReference: v1.SecretReference{
			Name:      secretName,
			Namespace: rdsInstance.Namespace,
		},
		Key: "password",
	}

	return nil
}

func createSecretLabels(rdsInstance *rdsdbaasv1alpha1.RDSInstance) map[string]string {
	return map[string]string{
		"managed-by":               "rds-dbaas-operator",
		"owner":                    rdsInstance.Name,
		"owner.kind":               rdsInstance.Kind,
		"owner.namespace":          rdsInstance.Namespace,
		dbaasv1alpha1.TypeLabelKey: dbaasv1alpha1.TypeLabelValue,
	}
}

func setDBInstancePhase(dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) {
	switch *dbInstance.Status.DBInstanceStatus {
	case "available":
		rdsInstance.Status.Phase = phaseReady
	case "creating":
		rdsInstance.Status.Phase = phaseCreating
	case "deleting":
		rdsInstance.Status.Phase = phaseDeleting
	case "failed":
		rdsInstance.Status.Phase = phaseFailed
	case "inaccessible-encryption-credentials-recoverable", "incompatible-parameters", "restore-error":
		rdsInstance.Status.Phase = phaseError
	case "backing-up", "configuring-enhanced-monitoring", "configuring-iam-database-auth", "configuring-log-exports",
		"converting-to-vpc", "maintenance", "modifying", "moving-to-vpc", "rebooting", "resetting-master-credentials",
		"renaming", "starting", "stopping", "storage-optimization", "upgrading":
		rdsInstance.Status.Phase = phaseUpdating
	case "inaccessible-encryption-credentials", "incompatible-network", "incompatible-option-group", "incompatible-restore",
		"insufficient-capacity", "stopped", "storage-full":
		rdsInstance.Status.Phase = phaseUnknown
	default:
		rdsInstance.Status.Phase = phaseUnknown
	}
}

func setDBInstanceStatus(dbInstance *rdsv1alpha1.DBInstance, rdsInstance *rdsdbaasv1alpha1.RDSInstance) {
	if dbInstance.Status.ACKResourceMetadata != nil {
		if dbInstance.Status.ACKResourceMetadata.ARN != nil {
			rdsInstance.Status.InstanceInfo["ackResourceMetadata.arn"] = string(*dbInstance.Status.ACKResourceMetadata.ARN)
		}
		if dbInstance.Status.ACKResourceMetadata.OwnerAccountID != nil {
			rdsInstance.Status.InstanceInfo["ackResourceMetadata.ownerAccountID"] = string(*dbInstance.Status.ACKResourceMetadata.OwnerAccountID)
		}
		if dbInstance.Status.ACKResourceMetadata.Region != nil {
			rdsInstance.Status.InstanceInfo["ackResourceMetadata.region"] = string(*dbInstance.Status.ACKResourceMetadata.Region)
		}
	}
	if dbInstance.Status.ActivityStreamEngineNativeAuditFieldsIncluded != nil {
		rdsInstance.Status.InstanceInfo["activityStreamEngineNativeAuditFieldsIncluded"] = strconv.FormatBool(*dbInstance.Status.ActivityStreamEngineNativeAuditFieldsIncluded)
	}
	if dbInstance.Status.ActivityStreamKinesisStreamName != nil {
		rdsInstance.Status.InstanceInfo["activityStreamKinesisStreamName"] = *dbInstance.Status.ActivityStreamKinesisStreamName
	}
	if dbInstance.Status.ActivityStreamKMSKeyID != nil {
		rdsInstance.Status.InstanceInfo["activityStreamKMSKeyID"] = *dbInstance.Status.ActivityStreamKMSKeyID
	}
	if dbInstance.Status.ActivityStreamMode != nil {
		rdsInstance.Status.InstanceInfo["activityStreamMode"] = *dbInstance.Status.ActivityStreamMode
	}
	if dbInstance.Status.ActivityStreamStatus != nil {
		rdsInstance.Status.InstanceInfo["activityStreamStatus"] = *dbInstance.Status.ActivityStreamStatus
	}
	if dbInstance.Status.AssociatedRoles != nil {
		for i, r := range dbInstance.Status.AssociatedRoles {
			if r.FeatureName != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("associatedRoles[%d].featureName", i)] = *r.FeatureName
			}
			if r.RoleARN != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("associatedRoles[%d].roleARN", i)] = *r.RoleARN
			}
			if r.Status != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("associatedRoles[%d].status", i)] = *r.Status
			}
		}
	}
	if dbInstance.Status.AutomaticRestartTime != nil {
		rdsInstance.Status.InstanceInfo["automaticRestartTime"] = dbInstance.Status.AutomaticRestartTime.String()
	}
	if dbInstance.Status.AutomationMode != nil {
		rdsInstance.Status.InstanceInfo["automationMode"] = *dbInstance.Status.AutomationMode
	}
	if dbInstance.Status.AWSBackupRecoveryPointARN != nil {
		rdsInstance.Status.InstanceInfo["awsBackupRecoveryPointARN"] = *dbInstance.Status.AWSBackupRecoveryPointARN
	}
	if dbInstance.Status.CACertificateIdentifier != nil {
		rdsInstance.Status.InstanceInfo["caCertificateIdentifier"] = *dbInstance.Status.CACertificateIdentifier
	}
	if dbInstance.Status.CustomerOwnedIPEnabled != nil {
		rdsInstance.Status.InstanceInfo["customerOwnedIPEnabled"] = strconv.FormatBool(*dbInstance.Status.CustomerOwnedIPEnabled)
	}
	if dbInstance.Status.DBInstanceAutomatedBackupsReplications != nil {
		for i, r := range dbInstance.Status.DBInstanceAutomatedBackupsReplications {
			if r.DBInstanceAutomatedBackupsARN != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbInstanceAutomatedBackupsReplications[%d].dbInstanceAutomatedBackupsARN", i)] = *r.DBInstanceAutomatedBackupsARN
			}
		}
	}
	if dbInstance.Status.DBInstanceStatus != nil {
		rdsInstance.Status.InstanceInfo["dbInstanceStatus"] = *dbInstance.Status.DBInstanceStatus
	}
	if dbInstance.Status.DBParameterGroups != nil {
		for i, g := range dbInstance.Status.DBParameterGroups {
			if g.DBParameterGroupName != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbParameterGroups[%d].dbParameterGroupName", i)] = *g.DBParameterGroupName
			}
			if g.ParameterApplyStatus != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbParameterGroups[%d].parameterApplyStatus", i)] = *g.ParameterApplyStatus
			}
		}
	}
	if dbInstance.Status.DBSubnetGroup != nil {
		if dbInstance.Status.DBSubnetGroup.DBSubnetGroupARN != nil {
			rdsInstance.Status.InstanceInfo["dbSubnetGroup.dbSubnetGroupARN"] = *dbInstance.Status.DBSubnetGroup.DBSubnetGroupARN
		}
		if dbInstance.Status.DBSubnetGroup.DBSubnetGroupDescription != nil {
			rdsInstance.Status.InstanceInfo["dbSubnetGroup.dbSubnetGroupDescription"] = *dbInstance.Status.DBSubnetGroup.DBSubnetGroupDescription
		}
		if dbInstance.Status.DBSubnetGroup.DBSubnetGroupName != nil {
			rdsInstance.Status.InstanceInfo["dbSubnetGroup.dbSubnetGroupName"] = *dbInstance.Status.DBSubnetGroup.DBSubnetGroupName
		}
		if dbInstance.Status.DBSubnetGroup.SubnetGroupStatus != nil {
			rdsInstance.Status.InstanceInfo["dbSubnetGroup.subnetGroupStatus"] = *dbInstance.Status.DBSubnetGroup.SubnetGroupStatus
		}
		if dbInstance.Status.DBSubnetGroup.Subnets != nil {
			for i, s := range dbInstance.Status.DBSubnetGroup.Subnets {
				if s.SubnetAvailabilityZone != nil {
					if s.SubnetAvailabilityZone.Name != nil {
						rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetAvailabilityZone.name", i)] = *s.SubnetAvailabilityZone.Name
					}
				}
				if s.SubnetIdentifier != nil {
					rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetIdentifier", i)] = *s.SubnetIdentifier
				}
				if s.SubnetOutpost != nil {
					if s.SubnetOutpost.ARN != nil {
						rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetOutpost.arn", i)] = *s.SubnetOutpost.ARN
					}
				}
				if s.SubnetStatus != nil {
					rdsInstance.Status.InstanceInfo[fmt.Sprintf("dbSubnetGroup.subnets[%d].subnetStatus", i)] = *s.SubnetStatus
				}
			}
		}
		if dbInstance.Status.DBSubnetGroup.VPCID != nil {
			rdsInstance.Status.InstanceInfo["dbSubnetGroup.vpcID"] = *dbInstance.Status.DBSubnetGroup.VPCID
		}
	}
	if dbInstance.Status.DBInstancePort != nil {
		rdsInstance.Status.InstanceInfo["dbInstancePort"] = strconv.FormatInt(*dbInstance.Status.DBInstancePort, 10)
	}
	if dbInstance.Status.DBIResourceID != nil {
		rdsInstance.Status.InstanceInfo["dbiResourceID"] = *dbInstance.Status.DBIResourceID
	}
	if dbInstance.Status.DomainMemberships != nil {
		for i, m := range dbInstance.Status.DomainMemberships {
			if m.Domain != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("domainMemberships[%d].domain", i)] = *m.Domain
			}
			if m.FQDN != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("domainMemberships[%d].fQDN", i)] = *m.FQDN
			}
			if m.IAMRoleName != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("domainMemberships[%d].iamRoleName", i)] = *m.IAMRoleName
			}
			if m.Status != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("domainMemberships[%d].status", i)] = *m.Status
			}
		}
	}
	if dbInstance.Status.EnabledCloudwatchLogsExports != nil {
		for i, e := range dbInstance.Status.EnabledCloudwatchLogsExports {
			if e != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("enabledCloudwatchLogsExports[%d]", i)] = *e
			}
		}
	}
	if dbInstance.Status.Endpoint != nil {
		if dbInstance.Status.Endpoint.Address != nil {
			rdsInstance.Status.InstanceInfo["endpoint.address"] = *dbInstance.Status.Endpoint.Address
		}
		if dbInstance.Status.Endpoint.HostedZoneID != nil {
			rdsInstance.Status.InstanceInfo["endpoint.hostedZoneID"] = *dbInstance.Status.Endpoint.HostedZoneID
		}
		if dbInstance.Status.Endpoint.Port != nil {
			rdsInstance.Status.InstanceInfo["endpoint.port"] = strconv.FormatInt(*dbInstance.Status.Endpoint.Port, 10)
		}
	}
	if dbInstance.Status.EnhancedMonitoringResourceARN != nil {
		rdsInstance.Status.InstanceInfo["enhancedMonitoringResourceARN"] = *dbInstance.Status.EnhancedMonitoringResourceARN
	}
	if dbInstance.Status.IAMDatabaseAuthenticationEnabled != nil {
		rdsInstance.Status.InstanceInfo["iamDatabaseAuthenticationEnabled"] = strconv.FormatBool(*dbInstance.Status.IAMDatabaseAuthenticationEnabled)
	}
	if dbInstance.Status.InstanceCreateTime != nil {
		rdsInstance.Status.InstanceInfo["instanceCreateTime"] = dbInstance.Status.InstanceCreateTime.String()
	}
	if dbInstance.Status.LatestRestorableTime != nil {
		rdsInstance.Status.InstanceInfo["latestRestorableTime"] = dbInstance.Status.LatestRestorableTime.String()
	}
	if dbInstance.Status.ListenerEndpoint != nil {
		if dbInstance.Status.ListenerEndpoint.Address != nil {
			rdsInstance.Status.InstanceInfo["listenerEndpoint.address"] = *dbInstance.Status.ListenerEndpoint.Address
		}
		if dbInstance.Status.ListenerEndpoint.HostedZoneID != nil {
			rdsInstance.Status.InstanceInfo["listenerEndpoint.hostedZoneID"] = *dbInstance.Status.ListenerEndpoint.HostedZoneID
		}
		if dbInstance.Status.ListenerEndpoint.Port != nil {
			rdsInstance.Status.InstanceInfo["listenerEndpoint.port"] = strconv.FormatInt(*dbInstance.Status.ListenerEndpoint.Port, 10)
		}
	}
	if dbInstance.Status.OptionGroupMemberships != nil {
		for i, m := range dbInstance.Status.OptionGroupMemberships {
			if m.OptionGroupName != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("optionGroupMemberships[%d].optionGroupName", i)] = *m.OptionGroupName
			}
			if m.Status != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("optionGroupMemberships[%d].status", i)] = *m.Status
			}
		}
	}
	if dbInstance.Status.PendingModifiedValues != nil {
		if dbInstance.Status.PendingModifiedValues.AllocatedStorage != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.allocatedStorage"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.AllocatedStorage, 10)
		}
		if dbInstance.Status.PendingModifiedValues.AutomationMode != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.automationMode"] = *dbInstance.Status.PendingModifiedValues.AutomationMode
		}
		if dbInstance.Status.PendingModifiedValues.BackupRetentionPeriod != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.backupRetentionPeriod"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.BackupRetentionPeriod, 10)
		}
		if dbInstance.Status.PendingModifiedValues.CACertificateIdentifier != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.caCertificateIdentifier"] = *dbInstance.Status.PendingModifiedValues.CACertificateIdentifier
		}
		if dbInstance.Status.PendingModifiedValues.DBInstanceClass != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.dbInstanceClass"] = *dbInstance.Status.PendingModifiedValues.DBInstanceClass
		}
		if dbInstance.Status.PendingModifiedValues.DBInstanceIdentifier != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.dbInstanceIdentifier"] = *dbInstance.Status.PendingModifiedValues.DBInstanceIdentifier
		}
		if dbInstance.Status.PendingModifiedValues.DBSubnetGroupName != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.dbSubnetGroupName"] = *dbInstance.Status.PendingModifiedValues.DBSubnetGroupName
		}
		if dbInstance.Status.PendingModifiedValues.EngineVersion != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.engineVersion"] = *dbInstance.Status.PendingModifiedValues.EngineVersion
		}
		if dbInstance.Status.PendingModifiedValues.IAMDatabaseAuthenticationEnabled != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.iamDatabaseAuthenticationEnabled"] = strconv.FormatBool(*dbInstance.Status.PendingModifiedValues.IAMDatabaseAuthenticationEnabled)
		}
		if dbInstance.Status.PendingModifiedValues.IOPS != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.iops"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.IOPS, 10)
		}
		if dbInstance.Status.PendingModifiedValues.LicenseModel != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.licenseModel"] = *dbInstance.Status.PendingModifiedValues.LicenseModel
		}
		if dbInstance.Status.PendingModifiedValues.MasterUserPassword != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.masterUserPassword"] = *dbInstance.Status.PendingModifiedValues.MasterUserPassword
		}
		if dbInstance.Status.PendingModifiedValues.MultiAZ != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.multiAZ"] = strconv.FormatBool(*dbInstance.Status.PendingModifiedValues.MultiAZ)
		}
		if dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports != nil {
			if dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToDisable != nil {
				for i, d := range dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToDisable {
					if d != nil {
						rdsInstance.Status.InstanceInfo[fmt.Sprintf("pendingModifiedValues.pendingCloudwatchLogsExports.logTypesToDisable[%d]", i)] = *d
					}
				}
			}
			if dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToEnable != nil {
				for i, e := range dbInstance.Status.PendingModifiedValues.PendingCloudwatchLogsExports.LogTypesToEnable {
					if e != nil {
						rdsInstance.Status.InstanceInfo[fmt.Sprintf("pendingModifiedValues.pendingCloudwatchLogsExports.logTypesToEnable[%d]", i)] = *e
					}
				}
			}
		}
		if dbInstance.Status.PendingModifiedValues.Port != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.port"] = strconv.FormatInt(*dbInstance.Status.PendingModifiedValues.Port, 10)
		}
		if dbInstance.Status.PendingModifiedValues.ProcessorFeatures != nil {
			for i, f := range dbInstance.Status.PendingModifiedValues.ProcessorFeatures {
				if f.Name != nil {
					rdsInstance.Status.InstanceInfo[fmt.Sprintf("pendingModifiedValues.ProcessorFeature[%d].name", i)] = *f.Name
				}
				if f.Value != nil {
					rdsInstance.Status.InstanceInfo[fmt.Sprintf("pendingModifiedValues.ProcessorFeature[%d].value", i)] = *f.Value
				}
			}
		}
		if dbInstance.Status.PendingModifiedValues.ResumeFullAutomationModeTime != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.resumeFullAutomationModeTime"] = dbInstance.Status.PendingModifiedValues.ResumeFullAutomationModeTime.String()
		}
		if dbInstance.Status.PendingModifiedValues.StorageType != nil {
			rdsInstance.Status.InstanceInfo["pendingModifiedValues.storageType"] = *dbInstance.Status.PendingModifiedValues.StorageType
		}
	}
	if dbInstance.Status.PerformanceInsightsEnabled != nil {
		rdsInstance.Status.InstanceInfo["performanceInsightsEnabled"] = strconv.FormatBool(*dbInstance.Status.PerformanceInsightsEnabled)
	}
	if dbInstance.Status.ReadReplicaDBClusterIdentifiers != nil {
		for i, id := range dbInstance.Status.ReadReplicaDBClusterIdentifiers {
			if id != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("readReplicaDBClusterIdentifiers[%d]", i)] = *id
			}
		}
	}
	if dbInstance.Status.ReadReplicaDBInstanceIdentifiers != nil {
		for i, id := range dbInstance.Status.ReadReplicaDBInstanceIdentifiers {
			if id != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("readReplicaDBInstanceIdentifiers[%d]", i)] = *id
			}
		}
	}
	if dbInstance.Status.ReadReplicaSourceDBInstanceIdentifier != nil {
		rdsInstance.Status.InstanceInfo["readReplicaSourceDBInstanceIdentifier"] = *dbInstance.Status.ReadReplicaSourceDBInstanceIdentifier
	}
	if dbInstance.Status.ReplicaMode != nil {
		rdsInstance.Status.InstanceInfo["replicaMode"] = *dbInstance.Status.ReplicaMode
	}
	if dbInstance.Status.ResumeFullAutomationModeTime != nil {
		rdsInstance.Status.InstanceInfo["resumeFullAutomationModeTime"] = dbInstance.Status.ResumeFullAutomationModeTime.String()
	}
	if dbInstance.Status.SecondaryAvailabilityZone != nil {
		rdsInstance.Status.InstanceInfo["secondaryAvailabilityZone"] = *dbInstance.Status.SecondaryAvailabilityZone
	}
	if dbInstance.Status.StatusInfos != nil {
		for i, info := range dbInstance.Status.StatusInfos {
			if info.Message != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("statusInfos[%d].message", i)] = *info.Message
			}
			if info.Normal != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("statusInfos[%d].normal", i)] = strconv.FormatBool(*info.Normal)
			}
			if info.Status != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("statusInfos[%d].status", i)] = *info.Status
			}
			if info.StatusType != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("statusInfos[%d].statusType", i)] = *info.StatusType
			}
		}
	}
	if dbInstance.Status.TagList != nil {
		for i, l := range dbInstance.Status.TagList {
			if l.Key != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("tagList[%d].key", i)] = *l.Key
			}
			if l.Value != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("tagList[%d].value", i)] = *l.Value
			}
		}
	}
	if dbInstance.Status.VPCSecurityGroups != nil {
		for i, g := range dbInstance.Status.VPCSecurityGroups {
			if g.Status != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("vpcSecurityGroups[%d].status", i)] = *g.Status
			}
			if g.VPCSecurityGroupID != nil {
				rdsInstance.Status.InstanceInfo[fmt.Sprintf("vpcSecurityGroups[%d].vpcSecurityGroupID", i)] = *g.VPCSecurityGroupID
			}
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rdsdbaasv1alpha1.RDSInstance{}).
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
	if typeString, ok := object.GetAnnotations()[ophandler.TypeAnnotation]; ok && typeString == "RDSInstance.dbaas.redhat.com" {
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
