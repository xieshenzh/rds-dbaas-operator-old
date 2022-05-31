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

package v1alpha1_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	"github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

var _ = Describe("RDSInventoryWebhook", func() {
	Context("after creating RDSInventory", func() {
		rdsInventoryName := "rds-inventory-webhook"
		credentialsRefName := "credentials-ref-webhook"
		rdsInventory := &v1alpha1.RDSInventory{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rdsInventoryName,
				Namespace: testNamespace,
			},
			Spec: dbaasv1alpha1.DBaaSInventorySpec{
				CredentialsRef: &dbaasv1alpha1.NamespacedName{
					Namespace: testNamespace,
					Name:      credentialsRefName,
				},
			},
		}

		BeforeEach(func() {
			By("creating RDSInventory")
			Expect(k8sClient.Create(ctx, rdsInventory)).Should(Succeed())

			By("checking RDSInventory created")
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsInventory), &v1alpha1.RDSInventory{}); err != nil {
					return false
				}
				return true
			}, timeout).Should(BeTrue())
		})

		AfterEach(func() {
			By("deleting RDSInventory")
			Expect(k8sClient.Delete(ctx, rdsInventory)).Should(Succeed())

			By("checking RDSInventory deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsInventory), &v1alpha1.RDSInventory{})
				if err != nil && errors.IsNotFound(err) {
					return true
				}
				return false
			}, timeout).Should(BeTrue())
		})

		Context("after creating another RDSInventory", func() {
			It("should not allow creating RDSInventory", func() {
				rdsInventoryNotAllowedName := "rds-inventory-webhook-not-allowed"
				rdsInventoryNotAllowed := &v1alpha1.RDSInventory{
					ObjectMeta: metav1.ObjectMeta{
						Name:      rdsInventoryNotAllowedName,
						Namespace: testNamespace,
					},
					Spec: dbaasv1alpha1.DBaaSInventorySpec{
						CredentialsRef: &dbaasv1alpha1.NamespacedName{
							Namespace: testNamespace,
							Name:      credentialsRefName,
						},
					},
				}
				By("creating RDSInventory")
				Expect(k8sClient.Create(ctx, rdsInventoryNotAllowed)).Should(MatchError("admission webhook \"vrdsinventory.kb.io\" denied the request: " +
					"only one Inventory for RDS can exist in a cluster, there is already an Inventory rds-inventory-webhook created"))
			})
		})
	})
})
