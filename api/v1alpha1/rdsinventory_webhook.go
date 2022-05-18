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

package v1alpha1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var rdsinventorylog = logf.Log.WithName("rdsinventory-resource")

var inventoryWebhookApiClient client.Client

func (r *RDSInventory) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if inventoryWebhookApiClient == nil {
		inventoryWebhookApiClient = mgr.GetClient()
	}

	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-dbaas-redhat-com-v1alpha1-rdsinventory,mutating=false,failurePolicy=fail,sideEffects=None,groups=dbaas.redhat.com,resources=rdsinventories,verbs=create,versions=v1alpha1,name=vrdsinventory.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &RDSInventory{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *RDSInventory) ValidateCreate() error {
	rdsinventorylog.Info("validate create", "name", r.Name)
	return r.verifyInventoryCreated()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *RDSInventory) ValidateUpdate(old runtime.Object) error {
	rdsinventorylog.Info("validate update", "name", r.Name)
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *RDSInventory) ValidateDelete() error {
	rdsinventorylog.Info("validate delete", "name", r.Name)
	return nil
}

func (r *RDSInventory) verifyInventoryCreated() error {
	inventoryList := &RDSInventoryList{}
	if err := inventoryWebhookApiClient.List(context.TODO(), inventoryList); err != nil {
		return err
	}

	if len(inventoryList.Items) > 0 {
		return fmt.Errorf("only one Inventory for RDS can exist in a cluster, there is already an Inventory %s created", inventoryList.Items[0].Name)
	}

	return nil
}
