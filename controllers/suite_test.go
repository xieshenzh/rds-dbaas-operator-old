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
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	rdsv1alpha1 "github.com/aws-controllers-k8s/rds-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	rdsdbaasv1alpha1 "github.com/xieshenzh/rds-dbaas-operator/api/v1alpha1"
	"github.com/xieshenzh/rds-dbaas-operator/controllers"
	controllersrdstest "github.com/xieshenzh/rds-dbaas-operator/controllers/rds/test"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

const (
	testNamespace = "default"

	timeout = time.Second * 10
)

const (
	installNamespaceEnvVar = "INSTALL_NAMESPACE"
	operatorConditionName  = "OPERATOR_CONDITION_NAME"
	operatorConditionValue = "rds-dbaas-operator.v0.1.0"
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "config", "crd", "bases"),
			filepath.Join("..", "test", "crd"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = rdsdbaasv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = dbaasv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = rdsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = ackv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = appsv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = apiextensionsv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())
	Expect(mgr).ToNot(BeNil())

	err = os.Setenv(installNamespaceEnvVar, testNamespace)
	Expect(err).NotTo(HaveOccurred())

	err = os.Setenv(operatorConditionName, operatorConditionValue)
	Expect(err).NotTo(HaveOccurred())

	clientset, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(clientset).NotTo(BeNil())

	rdsDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ack-rds-controller",
			Namespace: testNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": "ack-rds-controller",
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"name": "ack-rds-controller",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            "ack-rds-controller",
							Image:           "quay.io/ecosystem-appeng/busybox",
							ImagePullPolicy: v1.PullIfNotPresent,
							Command:         []string{"sh", "-c", "echo The app is running! && sleep 3600"},
						},
					},
				},
			},
		},
	}
	assertResourceCreation(rdsDeployment)()

	providerReconciler := &controllers.DBaaSProviderReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Clientset: clientset,
	}
	err = providerReconciler.SetupWithManager(mgr)
	Expect(err).ToNot(HaveOccurred())

	inventoryReconciler := &controllers.RDSInventoryReconciler{
		Client:                             mgr.GetClient(),
		Scheme:                             mgr.GetScheme(),
		GetDescribeDBInstancesPaginatorAPI: controllersrdstest.NewMockDescribeDBInstancesPaginator,
		GetModifyDBInstanceAPI:             controllersrdstest.NewModifyDBInstance,
		ACKInstallNamespace:                testNamespace,
		RDSCRDFilePath:                     filepath.Join("..", "rds", "config", "common", "bases"),
	}
	err = inventoryReconciler.SetupWithManager(mgr)
	Expect(err).ToNot(HaveOccurred())

	connectionReconciler := &controllers.RDSConnectionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	err = connectionReconciler.SetupWithManager(mgr)
	Expect(err).ToNot(HaveOccurred())

	instanceReconciler := &controllers.RDSInstanceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	err = instanceReconciler.SetupWithManager(mgr)
	Expect(err).ToNot(HaveOccurred())

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(rdsDeployment), rdsDeployment)
	Expect(err).NotTo(HaveOccurred())
	Expect(*rdsDeployment.Spec.Replicas).Should(BeZero())

	adoptedresourcesCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "adoptedresources.services.k8s.aws",
		},
	}
	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(adoptedresourcesCRD), adoptedresourcesCRD)
	Expect(err).NotTo(HaveOccurred())

	fieldexportsCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fieldexports.services.k8s.aws",
		},
	}
	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(fieldexportsCRD), fieldexportsCRD)
	Expect(err).NotTo(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()
}, 60)

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

	err = os.Unsetenv(installNamespaceEnvVar)
	Expect(err).NotTo(HaveOccurred())
	err = os.Unsetenv(operatorConditionName)
	Expect(err).NotTo(HaveOccurred())
})

func assertResourceCreation(object client.Object) func() {
	return func() {
		By("creating resource")
		object.SetResourceVersion("")
		Expect(k8sClient.Create(ctx, object)).Should(Succeed())

		By("checking the resource created")
		Eventually(func() bool {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
				return false
			}
			return true
		}, timeout).Should(BeTrue())
	}
}

func assertResourceDeletion(object client.Object) func() {
	return func() {
		By("deleting resource")
		Expect(k8sClient.Delete(ctx, object)).Should(Succeed())

		By("checking the resource deleted")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(object), object)
			if err != nil && errors.IsNotFound(err) {
				return true
			}
			return false
		}, timeout).Should(BeTrue())
	}
}
