package controllers

import (
	"context"
	"time"

	marin3rv1alpha1 "github.com/3scale/marin3r/apis/marin3r/v1alpha1"
	xdss "github.com/3scale/marin3r/pkg/discoveryservice/xdss"
	xdss_v2 "github.com/3scale/marin3r/pkg/discoveryservice/xdss/v2"
	xdss_v3 "github.com/3scale/marin3r/pkg/discoveryservice/xdss/v3"
	"github.com/3scale/marin3r/pkg/envoy"
	testutil "github.com/3scale/marin3r/pkg/util/test"
	envoy_api_v2 "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_config_endpoint_v3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoy_extensions_transport_sockets_tls_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	cache_types "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache_v2 "github.com/envoyproxy/go-control-plane/pkg/cache/v2"
	cache_v3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/operator-framework/operator-lib/status"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("EnvoyConfigRevision controller", func() {
	var namespace string
	var nodeID string

	BeforeEach(func() {
		// Create a namespace for each block
		namespace = "test-ns-" + nameGenerator.Generate()
		// Create a nodeID for each block
		nodeID = nameGenerator.Generate()
		// Add any setup steps that needs to be executed before each test
		testNamespace := &v1.Namespace{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}

		err := k8sClient.Create(context.Background(), testNamespace)
		Expect(err).ToNot(HaveOccurred())

		n := &v1.Namespace{}
		Eventually(func() bool {
			err := k8sClient.Get(context.Background(), types.NamespacedName{Name: namespace}, n)
			if err != nil {
				return false
			}
			return true
		}, 30*time.Second, 5*time.Second).Should(BeTrue())

	})

	AfterEach(func() {
		// Delete the namespace
		testNamespace := &v1.Namespace{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}
		// Add any teardown steps that needs to be executed after each test
		err := k8sClient.Delete(context.Background(), testNamespace, client.PropagationPolicy(metav1.DeletePropagationForeground))
		Expect(err).ToNot(HaveOccurred())

		n := &v1.Namespace{}
		Eventually(func() bool {
			err := k8sClient.Get(context.Background(), types.NamespacedName{Name: namespace}, n)
			if err != nil && errors.IsNotFound(err) {
				return false
			}
			return true
		}, 30*time.Second, 5*time.Second).Should(BeTrue())
	})

	Context("using v2 envoy API version", func() {
		var ecr *marin3rv1alpha1.EnvoyConfigRevision

		BeforeEach(func() {
			By("creating a v2 EnvoyConfigRevision")
			ecr = &marin3rv1alpha1.EnvoyConfigRevision{
				ObjectMeta: metav1.ObjectMeta{Name: "ecr", Namespace: namespace},
				Spec: marin3rv1alpha1.EnvoyConfigRevisionSpec{
					NodeID:  nodeID,
					Version: "xxxx",
					EnvoyResources: &marin3rv1alpha1.EnvoyResources{
						Endpoints: []marin3rv1alpha1.EnvoyResource{
							{Name: "endpoint", Value: "{\"cluster_name\": \"endpoint\"}"},
						}}},
			}
			err := k8sClient.Create(context.Background(), ecr)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
				if err != nil {
					return false
				}
				return true
			}, 30*time.Second, 5*time.Second).Should(BeTrue())
		})

		When("RevisionPublished condition is false in EnvoyConfigRevision", func() {

			It("should not make changes to the xDS cache", func() {

				_, err := ecrV2Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
				Expect(err).To(HaveOccurred())
			})
		})

		When("RevisionPublished condition is true in EnvoyConfigRevision", func() {

			It("should update the xDS cache with new snapshot for the nodeID and do not modify the v3 xDS cache", func() {

				By("setting ECR RevisionPublished condition to true")
				ecr = &marin3rv1alpha1.EnvoyConfigRevision{}
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
					if err != nil {
						return false
					}
					return true
				}, 30*time.Second, 5*time.Second).Should(BeTrue())

				patch := client.MergeFrom(ecr.DeepCopy())
				ecr.Status.Conditions.SetCondition(status.Condition{
					Type:   marin3rv1alpha1.RevisionPublishedCondition,
					Status: corev1.ConditionTrue,
				})

				err := k8sClient.Status().Patch(context.Background(), ecr, patch)
				Expect(err).ToNot(HaveOccurred())
				Expect(ecr.Status.Conditions.IsTrueFor(marin3rv1alpha1.RevisionPublishedCondition)).To(BeTrue())

				By("checking that a snapshot for spec.nodeId exists in the v2 xDS cache")
				var gotV2Snap xdss.Snapshot
				Eventually(func() bool {
					gotV2Snap, err = ecrV2Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
					if err != nil {
						return false
					}
					return true
				}, 30*time.Second, 5*time.Second).Should(BeTrue())

				wantSnap := xdss_v2.NewSnapshot(&cache_v2.Snapshot{
					Resources: [6]cache_v2.Resources{
						{Version: "xxxx", Items: map[string]cache_types.Resource{
							"endpoint": &envoy_api_v2.ClusterLoadAssignment{ClusterName: "endpoint"}}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx-557db659d4", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					}})
				Expect(testutil.SnapshotsAreEqual(gotV2Snap, wantSnap)).To(BeTrue())

				By("checking that a snapshot for spec.nodeId does not exist in the v3 xDS cache")
				_, err = ecrV3Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
				Expect(err).To(HaveOccurred())

			})

		})
	})

	Context("using v3 envoy API version", func() {
		var ecr *marin3rv1alpha1.EnvoyConfigRevision

		BeforeEach(func() {
			By("creating a v3 EnvoyConfigRevision")
			ecr = &marin3rv1alpha1.EnvoyConfigRevision{
				ObjectMeta: metav1.ObjectMeta{Name: "ecr", Namespace: namespace},
				Spec: marin3rv1alpha1.EnvoyConfigRevisionSpec{
					NodeID:   nodeID,
					Version:  "xxxx",
					EnvoyAPI: pointer.StringPtr(string(envoy.APIv3)),
					EnvoyResources: &marin3rv1alpha1.EnvoyResources{
						Endpoints: []marin3rv1alpha1.EnvoyResource{
							{Name: "endpoint", Value: "{\"cluster_name\": \"endpoint\"}"},
						}}},
			}
			err := k8sClient.Create(context.Background(), ecr)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
				if err != nil {
					return false
				}
				return true
			}, 30*time.Second, 5*time.Second).Should(BeTrue())
		})

		When("RevisionPublished condition is false in EnvoyConfigRevision", func() {

			It("should not make changes to the xDS cache", func() {

				_, err := ecrV3Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
				Expect(err).To(HaveOccurred())
			})
		})

		When("RevisionPublished condition is true in EnvoyConfigRevision", func() {

			It("should update the xDS cache with new snapshot for the nodeID and do not modify the v3 xDS cache", func() {

				By("setting ECR RevisionPublished condition to true")
				ecr = &marin3rv1alpha1.EnvoyConfigRevision{}
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
					if err != nil {
						return false
					}
					return true
				}, 30*time.Second, 5*time.Second).Should(BeTrue())

				patch := client.MergeFrom(ecr.DeepCopy())
				ecr.Status.Conditions.SetCondition(status.Condition{
					Type:   marin3rv1alpha1.RevisionPublishedCondition,
					Status: corev1.ConditionTrue,
				})

				err := k8sClient.Status().Patch(context.Background(), ecr, patch)
				Expect(err).ToNot(HaveOccurred())
				Expect(ecr.Status.Conditions.IsTrueFor(marin3rv1alpha1.RevisionPublishedCondition)).To(BeTrue())

				By("checking that a snapshot for spec.nodeId exists in the v2 xDS cache")
				var gotV3Snap xdss.Snapshot
				Eventually(func() bool {
					gotV3Snap, err = ecrV3Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
					if err != nil {
						return false
					}
					return true
				}, 30*time.Second, 5*time.Second).Should(BeTrue())

				wantSnap := xdss_v3.NewSnapshot(&cache_v3.Snapshot{
					Resources: [6]cache_v3.Resources{
						{Version: "xxxx", Items: map[string]cache_types.Resource{
							"endpoint": &envoy_config_endpoint_v3.ClusterLoadAssignment{ClusterName: "endpoint"}}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx-557db659d4", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					}})
				Expect(testutil.SnapshotsAreEqual(gotV3Snap, wantSnap)).To(BeTrue())

				By("checking that a snapshot for spec.nodeId does not exist in the v2 xDS cache")
				_, err = ecrV2Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
				Expect(err).To(HaveOccurred())

			})

		})
	})

	Context("load certificates from secrets", func() {
		var ecr *marin3rv1alpha1.EnvoyConfigRevision

		BeforeEach(func() {
			By("creating a secret of 'kubernetes.io/tls' type")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: namespace},
				Type:       corev1.SecretTypeTLS,
				Data:       map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
			}
			err := k8sClient.Create(context.Background(), secret)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "secret", Namespace: namespace}, secret)
				if err != nil {
					return false
				}
				return true
			}, 30*time.Second, 5*time.Second).Should(BeTrue())

			By("creating a EnvoyConfigRevision with a reference to the created Secret")
			ecr = &marin3rv1alpha1.EnvoyConfigRevision{
				ObjectMeta: metav1.ObjectMeta{Name: "ecr", Namespace: namespace},
				Spec: marin3rv1alpha1.EnvoyConfigRevisionSpec{
					NodeID:   nodeID,
					Version:  "xxxx",
					EnvoyAPI: pointer.StringPtr(string(envoy.APIv3)),
					EnvoyResources: &marin3rv1alpha1.EnvoyResources{
						Secrets: []marin3rv1alpha1.EnvoySecretResource{{
							Name: "secret",
							Ref:  corev1.SecretReference{Name: "secret", Namespace: namespace}},
						},
					},
				},
			}
			err = k8sClient.Create(context.Background(), ecr)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
				if err != nil {
					return false
				}
				return true
			}, 30*time.Second, 5*time.Second).Should(BeTrue())

			By("settign the EnvoyConfigRevision as published")
			patch := client.MergeFrom(ecr.DeepCopy())
			ecr.Status.Conditions.SetCondition(status.Condition{
				Type:   marin3rv1alpha1.RevisionPublishedCondition,
				Status: corev1.ConditionTrue,
			})
			err = k8sClient.Status().Patch(context.Background(), ecr, patch)
			Expect(err).ToNot(HaveOccurred())
			Expect(ecr.Status.Conditions.IsTrueFor(marin3rv1alpha1.RevisionPublishedCondition)).To(BeTrue())

			wantSnap := xdss_v3.NewSnapshot(&cache_v3.Snapshot{
				Resources: [6]cache_v3.Resources{
					{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					{Version: "xxxx-77c9875d7b", Items: map[string]cache_types.Resource{
						"secret": &envoy_extensions_transport_sockets_tls_v3.Secret{
							Name: "secret",
							Type: &envoy_extensions_transport_sockets_tls_v3.Secret_TlsCertificate{
								TlsCertificate: &envoy_extensions_transport_sockets_tls_v3.TlsCertificate{
									PrivateKey: &envoy_config_core_v3.DataSource{
										Specifier: &envoy_config_core_v3.DataSource_InlineBytes{InlineBytes: []byte("key")},
									},
									CertificateChain: &envoy_config_core_v3.DataSource{
										Specifier: &envoy_config_core_v3.DataSource_InlineBytes{InlineBytes: []byte("cert")},
									}}}}}},
					{Version: "xxxx", Items: map[string]cache_types.Resource{}},
				}})

			By("waiting for the envoy resources to be published in the xDS cache")
			Eventually(func() bool {
				gotV3Snap, err := ecrV3Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
				if err != nil {
					return false
				}
				return testutil.SnapshotsAreEqual(gotV3Snap, wantSnap)
			}, 30*time.Second, 5*time.Second).Should(BeTrue())
		})

		When("Secret changes", func() {

			It("should update the xDS cache with new snapshot for the nodeID", func() {
				By("updating the certificate contained in the Secret resource")
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: namespace},
					Type:       corev1.SecretTypeTLS,
					Data:       map[string][]byte{"tls.crt": []byte("new-cert"), "tls.key": []byte("new-key")},
				}
				err := k8sClient.Update(context.Background(), secret)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func() bool {
					err = k8sClient.Get(context.Background(), types.NamespacedName{Name: "secret", Namespace: namespace}, secret)
					if string(secret.Data["tls.crt"]) == "new-cert" {
						return true
					}
					return false
				}, 30*time.Second, 5*time.Second).Should(BeTrue())

				wantSnap := xdss_v3.NewSnapshot(&cache_v3.Snapshot{
					Resources: [6]cache_v3.Resources{
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
						{Version: "xxxx-679f7cbbfd", Items: map[string]cache_types.Resource{
							"secret": &envoy_extensions_transport_sockets_tls_v3.Secret{
								Name: "secret",
								Type: &envoy_extensions_transport_sockets_tls_v3.Secret_TlsCertificate{
									TlsCertificate: &envoy_extensions_transport_sockets_tls_v3.TlsCertificate{
										PrivateKey: &envoy_config_core_v3.DataSource{
											Specifier: &envoy_config_core_v3.DataSource_InlineBytes{InlineBytes: []byte("new-key")},
										},
										CertificateChain: &envoy_config_core_v3.DataSource{
											Specifier: &envoy_config_core_v3.DataSource_InlineBytes{InlineBytes: []byte("new-cert")},
										}}}}}},
						{Version: "xxxx", Items: map[string]cache_types.Resource{}},
					}})

				By("checking the new certificate it's in the xDS cache")
				Eventually(func() bool {
					gotV3Snap, err := ecrV3Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
					if err != nil {
						return false
					}
					return testutil.SnapshotsAreEqual(gotV3Snap, wantSnap)
				}, 30*time.Second, 5*time.Second).Should(BeTrue())

			})

		})
	})

	Context("EnvoyConfigRevision finalizer", func() {
		var ecr *marin3rv1alpha1.EnvoyConfigRevision

		BeforeEach(func() {
			By("creating an EnvoyConfigRevision")
			ecr = &marin3rv1alpha1.EnvoyConfigRevision{
				ObjectMeta: metav1.ObjectMeta{Name: "ecr", Namespace: namespace},
				Spec: marin3rv1alpha1.EnvoyConfigRevisionSpec{
					NodeID:  nodeID,
					Version: "xxxx",
					EnvoyResources: &marin3rv1alpha1.EnvoyResources{
						Endpoints: []marin3rv1alpha1.EnvoyResource{
							{Name: "endpoint", Value: "{\"cluster_name\": \"endpoint\"}"},
						}}},
			}
			err := k8sClient.Create(context.Background(), ecr)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
				if err != nil {
					return false
				}
				return true
			}, 30*time.Second, 5*time.Second).Should(BeTrue())
		})

		When("resource is created", func() {

			It("should have a finalizer", func() {
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
					Expect(err).ToNot(HaveOccurred())
					if len(ecr.GetFinalizers()) == 1 {
						return true
					}
					return false
				}, 30*time.Second, 5*time.Second).Should(BeTrue())
			})
		})

		When("resource is deleted", func() {

			BeforeEach(func() {
				By("setting the published condition in the EnvoyConfigRevision to force execution of the finalizer code")
				patch := client.MergeFrom(ecr.DeepCopy())
				ecr.Status.Conditions.SetCondition(status.Condition{
					Type:   marin3rv1alpha1.RevisionPublishedCondition,
					Status: corev1.ConditionTrue,
				})
				err := k8sClient.Status().Patch(context.Background(), ecr, patch)
				Expect(err).ToNot(HaveOccurred())

				By("waiting for the EnvoyConfigRevision to get published")
				Eventually(func() bool {
					_, err := ecrV2Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
					if err != nil {
						return false
					}
					return true
				}, 300*time.Second, 5*time.Second).Should(BeTrue())

				Expect(k8sClient.Delete(context.Background(), ecr)).Should(Succeed())
			})

			Specify("Snapshot for the nodeID should have been cleared in the xDS cache", func() {
				Eventually(func() bool {
					_, err := ecrV2Reconciler.XdsCache.GetSnapshot(ecr.Spec.NodeID)
					if err != nil {
						return true
					}
					return false
				}, 30*time.Second, 5*time.Second).Should(BeTrue())
			})
		})
	})

	Context("EnvoyConfigRevision taints", func() {
		var ecr *marin3rv1alpha1.EnvoyConfigRevision

		BeforeEach(func() {
			By("creating an EnvoyConfigRevision")
			ecr = &marin3rv1alpha1.EnvoyConfigRevision{
				ObjectMeta: metav1.ObjectMeta{Name: "ecr", Namespace: namespace},
				Spec: marin3rv1alpha1.EnvoyConfigRevisionSpec{
					NodeID:  nodeID,
					Version: "xxxx",
					EnvoyResources: &marin3rv1alpha1.EnvoyResources{
						Endpoints: []marin3rv1alpha1.EnvoyResource{
							{Name: "endpoint", Value: "{\"cluster_name\": \"endpoint\"}"},
						}}},
			}
			err := k8sClient.Create(context.Background(), ecr)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
				if err != nil {
					return false
				}
				return true
			}, 30*time.Second, 5*time.Second).Should(BeTrue())
		})

		When("RevisionTainted condition is true", func() {

			BeforeEach(func() {
				By("setting the RevisionTained condition in the EnvoyConfigRevision")
				patch := client.MergeFrom(ecr.DeepCopy())
				ecr.Status.Conditions.SetCondition(status.Condition{
					Type:   marin3rv1alpha1.RevisionTaintedCondition,
					Status: corev1.ConditionTrue,
				})
				err := k8sClient.Status().Patch(context.Background(), ecr, patch)
				Expect(err).ToNot(HaveOccurred())

			})

			Specify("status.tainted should be true", func() {
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
					Expect(err).ToNot(HaveOccurred())
					if ecr.Status.Tainted {
						return true
					}
					return false
				}, 30*time.Second, 5*time.Second).Should(BeTrue())
			})

			Specify("status.tainted should be false when condition is cleared", func() {

				By("unsetting the RevisionTained condition in the EnvoyConfigRevision")
				patch := client.MergeFrom(ecr.DeepCopy())
				ecr.Status.Conditions.SetCondition(status.Condition{
					Type:   marin3rv1alpha1.RevisionTaintedCondition,
					Status: corev1.ConditionFalse,
				})
				err := k8sClient.Status().Patch(context.Background(), ecr, patch)
				Expect(err).ToNot(HaveOccurred())

				By("checking the status.Tainded field in the EnvoyConfigCache")
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "ecr", Namespace: namespace}, ecr)
					Expect(err).ToNot(HaveOccurred())
					if !ecr.Status.Tainted {
						return true
					}
					return false
				}, 30*time.Second, 5*time.Second).Should(BeTrue())
			})
		})

	})

})
