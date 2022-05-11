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

	apiv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	opv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	ophandler "github.com/operator-framework/operator-lib/handler"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

const (
	rdsInventoryType = "RDSInventory.dbaas.redhat.com"

	inventoryFinalizer = "rds.dbaas.redhat.com/inventory"

	awsAccessKeyID     = "AWS_ACCESS_KEY_ID"
	awsSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	awsRegion          = "AWS_REGION"
	ackResourceTags    = "ACK_RESOURCE_TAGS"
	ackLogLevel        = "ACK_LOG_LEVEL"

	secretName    = "ack-user-secrets"
	configmapName = "ack-user-config"

	adoptedResourceCRDFile = "services.k8s.aws_adoptedresources.yaml"
	adoptedResourceCRDName = "adoptedresources.services.k8s.aws"
	catalogName            = "rds-catalogsource"
	catalogNamespace       = "openshift-marketplace"
	subscriptionName       = "rds-subscription"
	subscriptionPackage    = "ack-rds-controller"
	subscriptionChannel    = "alpha"
	deploymentName         = "ack-rds-controller"
	csvName                = "ack-rds-controller.v0.0.24"

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
//+kubebuilder:rbac:groups=rds.services.k8s.aws,resources=dbinstances,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=services.k8s.aws,resources=adoptedresources,verbs=get;list;watch;create
//+kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=get;list;create;update;watch;delete
//+kubebuilder:rbac:groups=operators.coreos.com,resources=clusterserviceversions,verbs=get;update;delete
//+kubebuilder:rbac:groups=operators.coreos.com,resources=clusterserviceversions/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;create;update;watch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *RDSInventoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	var inventory rdsdbaasv1alpha1.RDSInventory

	checkFinalizer := func() bool {
		if inventory.ObjectMeta.DeletionTimestamp.IsZero() {
			if !controllerutil.ContainsFinalizer(&inventory, inventoryFinalizer) {
				controllerutil.AddFinalizer(&inventory, inventoryFinalizer)
				if e := r.Update(ctx, &inventory); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Inventory modified, retry reconciling")
						//TODO
						return true
					}
					logger.Error(e, "Failed to add finalizer to Inventory")
					//TODO
					return true
				}
				logger.Info("Finalizer added to Inventory")
				//TODO
				return true
			}
		} else {
			if controllerutil.ContainsFinalizer(&inventory, inventoryFinalizer) {
				secret := &v1.Secret{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: secretName}, secret); e != nil {
					if !errors.IsNotFound(e) {
						//TODO
					}
				} else {
					if e := r.Delete(ctx, secret); e != nil {
						//TODO
					}
					//TODO
				}
				configmap := &v1.ConfigMap{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: configmapName}, configmap); e != nil {
					if !errors.IsNotFound(e) {
						//TODO
					}
				} else {
					if e := r.Delete(ctx, configmap); e != nil {
						//TODO
					}
					//TODO
				}
				crd := &apiextensionsv1.CustomResourceDefinition{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: adoptedResourceCRDName}, crd); e != nil {
					if !errors.IsNotFound(e) {
						//TODO
					}
				} else {
					if e := r.Delete(ctx, crd); e != nil {
						//TODO
					}
					//TODO
				}
				subscription := &opv1alpha1.Subscription{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: subscriptionName}, subscription); e != nil {
					if !errors.IsNotFound(e) {
						//TODO
					}
				} else {
					if e := r.Delete(ctx, subscription); e != nil {
						//TODO
					}
					//TODO
				}
				csv := &opv1alpha1.ClusterServiceVersion{}
				if e := r.Get(ctx, client.ObjectKey{Namespace: r.ACKInstallNamespace, Name: csvName}, csv); e != nil {
					if !errors.IsNotFound(e) {
						//TODO
					}
				} else {
					if e := r.Delete(ctx, csv); e != nil {
						//TODO
					}
					//TODO
				}

				controllerutil.RemoveFinalizer(&inventory, inventoryFinalizer)
				if e := r.Update(ctx, &inventory); e != nil {
					if errors.IsConflict(e) {
						logger.Info("Inventory modified, retry reconciling")
						//TODO
						return true
					}
					logger.Error(e, "Failed to remove finalizer from Inventory")
					//TODO
					return true
				}
				//TODO
				return true
			}

			// Stop reconciliation as the item is being deleted
			//TODO
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

	if checkFinalizer() {
		return
	}

	credentialsRef := &v1.Secret{}
	if e := r.Get(ctx, client.ObjectKey{Namespace: inventory.Spec.CredentialsRef.Namespace,
		Name: inventory.Spec.CredentialsRef.Name}, credentialsRef); e != nil {
		logger.Error(e, "Failed to get credentials reference for Inventory")
		if errors.IsNotFound(e) {
			//TODO
		}
		//TODO
	}
	var accessKey, secretKey, region string
	if ak, ok := credentialsRef.Data[awsAccessKeyID]; !ok || len(ak) == 0 {
		//TODO
	} else {
		if key, e := parseBase64(ak); e != nil {
			//TODO
		} else {
			accessKey = key
		}
	}
	if sk, ok := credentialsRef.Data[awsSecretAccessKey]; !ok || len(sk) == 0 {
		//TODO
	} else {
		if key, e := parseBase64(sk); e != nil {
			//TODO
		} else {
			secretKey = key
		}
	}
	if r, ok := credentialsRef.Data[awsRegion]; !ok || len(r) == 0 {
		//TODO
	} else {
		if rg, e := parseBase64(r); e != nil {
			//TODO
		} else {
			region = rg
		}
	}

	if e := r.createOrUpdateSecret(ctx, &inventory, credentialsRef); e != nil {
		//TODO
	}
	if e := r.createOrUpdateConfigMap(ctx, &inventory, credentialsRef); e != nil {
		//TODO
	}

	if e := r.installCRD(ctx, &inventory, adoptedResourceCRDFile); e != nil {
		//TODO
	}
	if e := r.installSubscription(ctx, &inventory); e != nil {
		//TODO
	}
	if r, e := r.waitForOperator(ctx); e != nil {
		//TODO
	} else if !r {
		//TODO
	}
	if r, e := r.waitForCSV(ctx, &inventory); e != nil {
		//TODO
	} else if !r {
		//TODO
	}

	var awsDBInstances []rdstypesv2.DBInstance
	awsClient := rds.New(rds.Options{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	})
	paginator := rds.NewDescribeDBInstancesPaginator(awsClient, nil)
	for paginator.HasMorePages() {
		if output, e := paginator.NextPage(ctx); e != nil {
			//TODO
		} else if output != nil {
			awsDBInstances = append(awsDBInstances, output.DBInstances...)
		}
	}

	if awsDBInstances != nil && len(awsDBInstances) > 0 {
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
	}

	adoptedDBInstanceList := &rdsv1alpha1.DBInstanceList{}
	if e := r.List(ctx, adoptedDBInstanceList, client.InNamespace(inventory.Namespace),
		client.MatchingLabels(map[string]string{adpotedDBInstanceLabelKey: adpotedDBInstanceLabelValue})); e != nil {
		//TODO
	}

	for _, adoptedDBInstance := range adoptedDBInstanceList.Items {
		if adoptedDBInstance.Spec.MasterUsername == nil || adoptedDBInstance.Spec.MasterUserPassword == nil {
			if e := setCredentials(ctx, r.Client, r.Scheme, &adoptedDBInstance, inventory.Namespace, nil, ""); e != nil {
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
		if region, e := parseBase64(credentialsRef.Data[awsRegion]); e != nil {
			return e
		} else {
			cm.Data[awsRegion] = region
		}
		if l, ok := credentialsRef.Data[ackLogLevel]; ok {
			if level, e := parseBase64(l); e != nil {
				return e
			} else {
				cm.Data[ackLogLevel] = level
			}
		}
		if t, ok := credentialsRef.Data[ackResourceTags]; ok {
			if tags, e := parseBase64(t); e != nil {
				return e
			} else {
				cm.Data[ackResourceTags] = tags
			}
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

func (r *RDSInventoryReconciler) installCRD(ctx context.Context, inventory *rdsdbaasv1alpha1.RDSInventory, file string) error {
	crd, err := r.readCRDFile(file)
	if err != nil {
		return err
	}
	c := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crd.Name,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, c, func() error {
		c.Spec = crd.Spec
		if e := ophandler.SetOwnerAnnotations(inventory, c); e != nil {
			return e
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (r *RDSInventoryReconciler) readCRDFile(file string) (*apiextensionsv1.CustomResourceDefinition, error) {
	d, err := ioutil.ReadFile(file)
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

func (r *RDSInventoryReconciler) installSubscription(ctx context.Context, inventory *rdsdbaasv1alpha1.RDSInventory) error {
	subscription := &opv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:      subscriptionName,
			Namespace: r.ACKInstallNamespace,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, subscription, func() error {
		subscription.Spec = &opv1alpha1.SubscriptionSpec{
			CatalogSource:          catalogName,
			CatalogSourceNamespace: catalogNamespace,
			Package:                subscriptionPackage,
			Channel:                subscriptionChannel,
			InstallPlanApproval:    opv1alpha1.ApprovalAutomatic,
		}
		if err := ophandler.SetOwnerAnnotations(inventory, subscription); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (r *RDSInventoryReconciler) waitForOperator(ctx context.Context) (bool, error) {
	deployments := &apiv1.DeploymentList{}
	opts := &client.ListOptions{
		Namespace: r.ACKInstallNamespace,
	}
	if err := r.List(ctx, deployments, opts); err != nil {
		return false, err
	}
	for _, deployment := range deployments.Items {
		if deployment.Name == deploymentName {
			if deployment.Status.ReadyReplicas > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *RDSInventoryReconciler) waitForCSV(ctx context.Context, inventory *rdsdbaasv1alpha1.RDSInventory) (bool, error) {
	csv := &opv1alpha1.ClusterServiceVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.ACKInstallNamespace,
			Namespace: csvName,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(csv), csv); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if set := checkOwnerReferenceSet(inventory, csv); set {
		return true, nil
	}
	if err := ophandler.SetOwnerAnnotations(inventory, csv); err != nil {
		return false, err
	}
	if err := r.Update(ctx, csv); err != nil {
		return false, err
	}
	return false, nil
}

func checkOwnerReferenceSet(inventory *rdsdbaasv1alpha1.RDSInventory, csv *opv1alpha1.ClusterServiceVersion) bool {
	if typeString, ok := csv.GetAnnotations()[ophandler.TypeAnnotation]; ok && typeString == rdsInventoryType {
		if namespacedNameString, ok := csv.GetAnnotations()[ophandler.NamespacedNameAnnotation]; ok &&
			namespacedNameString == fmt.Sprintf("%s/%s", inventory.Namespace, inventory.Name) {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *RDSInventoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
