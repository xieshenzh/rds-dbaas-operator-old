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

package controllers_test

import (
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
)

var _ = Describe("DBaaSProviderController", func() {
	Context("after creating the operator ClusterRole", func() {
		clusterRole := &rbac.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf(operatorConditionValue+"-%v", "cluster-role"),
				Namespace: "dbaas-operator",
				Labels: map[string]string{
					"olm.owner":      operatorConditionValue,
					"olm.owner.kind": "ClusterServiceVersion",
				},
			},
		}
		BeforeEach(assertResourceCreation(clusterRole))
		AfterEach(assertResourceDeletion(clusterRole))

		Context("after creating the operator Deployment", func() {
			operatorDeployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rds-dbaas-operator-deployment",
					Namespace: testNamespace,
					Labels: map[string]string{
						"olm.owner":           operatorConditionValue,
						"olm.owner.kind":      "ClusterServiceVersion",
						"olm.owner.namespace": testNamespace,
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: pointer.Int32(0),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"control-plane": "controller-manager",
						},
					},
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"control-plane": "controller-manager",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:            "rds-dbaas-controller",
									Image:           "quay.io/ecosystem-appeng/busybox",
									ImagePullPolicy: v1.PullIfNotPresent,
									Command:         []string{"sh", "-c", "echo The app is running! && sleep 3600"},
								},
							},
						},
					},
				},
			}
			BeforeEach(assertResourceCreation(operatorDeployment))
			AfterEach(assertResourceDeletion(operatorDeployment))

			It("should register DBaaSProvider", func() {
				rdsProvider := &dbaasv1alpha1.DBaaSProvider{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rds-registration",
						Namespace: testNamespace,
					},
				}
				By("check if the CRDB DBaaSProvider is created")
				Eventually(func() bool {
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsProvider), rdsProvider)
					return err == nil
				}, timeout).Should(BeTrue())

				By("Validating DBaasProvider details")
				Expect(rdsProvider.Spec.InventoryKind).Should(Equal("RDSInventory"))
				Expect(rdsProvider.Spec.ConnectionKind).Should(Equal("RDSConnection"))
				Expect(rdsProvider.Spec.InstanceKind).Should(Equal("RDSInstance"))
				Expect(rdsProvider.Spec.AllowsFreeTrial).Should(BeFalse())
				Expect(len(rdsProvider.Spec.CredentialFields)).Should(Equal(5))
				Expect(len(rdsProvider.Spec.InstanceParameterSpecs)).Should(Equal(12))

				assertResourceDeletion(rdsProvider)()
			})
		})
	})
})
