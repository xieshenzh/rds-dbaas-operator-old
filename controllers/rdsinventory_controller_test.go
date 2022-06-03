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
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
)

var _ = Describe("RDSInventoryController", func() {
	Context("when Secret for launching RDS controller is created", func() {
		credentialName := "credentials-ref-inventory-controller"
		inventoryName := "rds-inventory-inventory-controller"

		accessKey := "AKIAIOSFODNN7EXAMPLE"
		secretKey := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		region := "us-east-1"

		credential := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      credentialName,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"AWS_ACCESS_KEY_ID":     []byte(accessKey),
				"AWS_SECRET_ACCESS_KEY": []byte(secretKey), //#nosec G101
				"AWS_REGION":            []byte(region),
			},
		}
		BeforeEach(assertResourceCreation(credential))
		AfterEach(assertResourceDeletion(credential))

		Context("when Inventory is created", func() {
			inventory := &rdsdbaasv1alpha1.RDSInventory{
				ObjectMeta: metav1.ObjectMeta{
					Name:      inventoryName,
					Namespace: testNamespace,
				},
				Spec: dbaasv1alpha1.DBaaSInventorySpec{
					CredentialsRef: &dbaasv1alpha1.NamespacedName{
						Name:      credentialName,
						Namespace: testNamespace,
					},
				},
			}
			BeforeEach(assertResourceCreation(inventory))

			Context("when check the status of the Inventory", func() {
				AfterEach(assertResourceDeletion(inventory))

				dbInstance1 := &rdsv1alpha1.DBInstance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "db-instance-1",
						Namespace: testNamespace,
					},
					Spec: rdsv1alpha1.DBInstanceSpec{
						Engine:               pointer.String("postgres"),
						DBInstanceIdentifier: pointer.String("dbInstance1"),
						DBInstanceClass:      pointer.String("db.t3.micro"),
					},
				}
				BeforeEach(assertResourceCreation(dbInstance1))
				AfterEach(assertResourceDeletion(dbInstance1))
				BeforeEach(func() {
					dbInstance1.Status = rdsv1alpha1.DBInstanceStatus{
						DBInstanceStatus: pointer.String("available"),
					}
					Eventually(func() bool {
						err := k8sClient.Status().Update(ctx, dbInstance1)
						return err == nil
					}, timeout).Should(BeTrue())
				})

				dbInstance2 := &rdsv1alpha1.DBInstance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "db-instance-2",
						Namespace: testNamespace,
					},
					Spec: rdsv1alpha1.DBInstanceSpec{
						Engine:               pointer.String("mysql"),
						DBInstanceIdentifier: pointer.String("dbInstance2"),
						DBInstanceClass:      pointer.String("db.t3.small"),
					},
				}
				BeforeEach(assertResourceCreation(dbInstance2))
				AfterEach(assertResourceDeletion(dbInstance2))
				BeforeEach(func() {
					dbInstance2.Status = rdsv1alpha1.DBInstanceStatus{
						DBInstanceStatus: pointer.String("creating"),
					}
					Eventually(func() bool {
						err := k8sClient.Status().Update(ctx, dbInstance2)
						return err == nil
					}, timeout).Should(BeTrue())
				})

				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ack-rds-controller",
						Namespace: testNamespace,
					},
				}
				BeforeEach(func() {
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), deployment); err != nil {
							return false
						}
						deployment.Status = appsv1.DeploymentStatus{
							Replicas:      1,
							ReadyReplicas: 1,
						}
						err := k8sClient.Status().Update(ctx, deployment)
						return err == nil
					}, timeout).Should(BeTrue())
				})
				AfterEach(func() {
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), deployment); err != nil {
							return false
						}
						deployment.Status = appsv1.DeploymentStatus{
							Replicas:      0,
							ReadyReplicas: 0,
						}
						err := k8sClient.Status().Update(ctx, deployment)
						return err == nil
					}, timeout).Should(BeTrue())
				})

				It("should start the RDS controller, adopt the DB instances and sync DB instance status", func() {
					By("checking if the Secret for RDS controller is created")
					rdsSecret := &v1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ack-user-secrets",
							Namespace: testNamespace,
						},
					}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsSecret), rdsSecret); err != nil {
							return false
						}
						if ak, ok := rdsSecret.Data["AWS_ACCESS_KEY_ID"]; !ok || string(ak) != accessKey {
							return false
						}
						if sk, ok := rdsSecret.Data["AWS_SECRET_ACCESS_KEY"]; !ok || string(sk) != secretKey {
							return false
						}
						return true
					}, timeout).Should(BeTrue())

					By("checking if the ConfigMap for RDS controller is created")
					rdsConfigMap := &v1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ack-user-config",
							Namespace: testNamespace,
						},
					}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsConfigMap), rdsConfigMap); err != nil {
							return false
						}
						if r, ok := rdsConfigMap.Data["AWS_REGION"]; !ok || r != region {
							return false
						}
						return true
					}, timeout).Should(BeTrue())

					By("checking if the RDS controller is started")
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), deployment); err != nil {
							return false
						}
						return *deployment.Spec.Replicas == 1 && deployment.Status.Replicas == 1 && deployment.Status.ReadyReplicas == 1
					}, timeout).Should(BeTrue())

					By("checking Inventory status")
					inv := &rdsdbaasv1alpha1.RDSInventory{
						ObjectMeta: metav1.ObjectMeta{
							Name:      inventoryName,
							Namespace: testNamespace,
						},
					}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(inv), inv); err != nil {
							return false
						}
						if len(inv.Status.Instances) != 2 {
							return false
						}
						instancesMap := make(map[string]dbaasv1alpha1.Instance, 2)
						for i := range inv.Status.Instances {
							ins := inv.Status.Instances[i]
							instancesMap[ins.InstanceID] = ins
						}
						if ins, ok := instancesMap[*dbInstance1.Spec.DBInstanceIdentifier]; !ok {
							return false
						} else {
							if ins.Name != dbInstance1.Name {
								return false
							}
							if s, ok := ins.InstanceInfo["dbInstanceStatus"]; !ok || s != *dbInstance1.Status.DBInstanceStatus {
								return false
							}
						}
						if ins, ok := instancesMap[*dbInstance2.Spec.DBInstanceIdentifier]; !ok {
							return false
						} else {
							if ins.Name != dbInstance2.Name {
								return false
							}
							if s, ok := ins.InstanceInfo["dbInstanceStatus"]; !ok || s != *dbInstance2.Status.DBInstanceStatus {
								return false
							}
						}
						return true
					}, timeout).Should(BeTrue())
				})
			})

			Context("when the Inventory is deleted", func() {
				It("should delete the owned resources and stop the RDS controller", func() {
					assertResourceDeletion(inventory)()

					By("checking if the Secret for RDS controller is deleted")
					rdsSecret := &v1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ack-user-secrets",
							Namespace: testNamespace,
						},
					}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsSecret), rdsSecret); err != nil {
							if errors.IsNotFound(err) {
								return true
							}
						}
						return false
					}, timeout).Should(BeTrue())

					By("checking if the ConfigMap for RDS controller is deleted")
					rdsConfigMap := &v1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ack-user-config",
							Namespace: testNamespace,
						},
					}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsConfigMap), rdsConfigMap); err != nil {
							if errors.IsNotFound(err) {
								return true
							}
						}
						return false
					}, timeout).Should(BeTrue())

					By("checking if the RDS controller is stopped")
					deployment := &appsv1.Deployment{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "ack-rds-controller",
							Namespace: testNamespace,
						},
					}
					Eventually(func() bool {
						err := k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), deployment)
						if err != nil {
							return false
						}
						return *deployment.Spec.Replicas == 0
					}, timeout).Should(BeTrue())
				})
			})
		})
	})
})
