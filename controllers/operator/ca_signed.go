package controllers

import (
	"context"
	"crypto/x509"
	"time"

	operatorv1alpha1 "github.com/3scale/marin3r/apis/operator/v1alpha1"
	"github.com/3scale/marin3r/pkg/common"
	"github.com/3scale/marin3r/pkg/util/pki"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *DiscoveryServiceCertificateReconciler) reconcileCASignedCertificate(ctx context.Context, dsc *operatorv1alpha1.DiscoveryServiceCertificate, log logr.Logger) error {

	// Get the issuer certificate
	issuerCert, issuerKey, err := r.getCACertificate(ctx, dsc.Spec)
	if err != nil {
		return err
	}

	secret := &corev1.Secret{}
	err = r.Client.Get(ctx,
		types.NamespacedName{
			Name:      dsc.Spec.SecretRef.Name,
			Namespace: dsc.Spec.SecretRef.Namespace,
		},
		secret)

	if err != nil {
		if errors.IsNotFound(err) {
			secret, err := genCASignedCertificateObject(dsc.Spec, issuerCert, issuerKey)
			if err != nil {
				return err
			}
			if err := controllerutil.SetControllerReference(dsc, secret, r.Scheme); err != nil {
				return err
			}
			if err := r.Client.Create(ctx, secret); err != nil {
				return err
			}
			log.Info("Created ca-signed certificate")
			return nil
		}
		return err
	}

	// Don't reconcile if renewal is disabled
	if dsc.Spec.CertificateRenewalConfig != nil && !dsc.Spec.CertificateRenewalConfig.Enabled {
		return nil
	}

	// Load the certificate
	cert, err := pki.LoadX509Certificate(secret.Data["tls.crt"])
	if err != nil {
		return err
	}

	// Check if certificate is invalid
	err = pki.Verify(cert, cert)
	if err != nil {
		log.Error(err, "Invalid certificate detected")
	}

	// If certificate is invalid or has been marked for renewal, reissue it
	if err != nil || dsc.Status.Conditions.IsTrueFor(operatorv1alpha1.CertificateNeedsRenewalCondition) {
		new, err := genCASignedCertificateObject(dsc.Spec, issuerCert, issuerKey)
		if err != nil {
			return err
		}
		patch := client.MergeFrom(secret.DeepCopy())
		secret.Data = new.Data
		if err := r.Client.Patch(ctx, secret, patch); err != nil {
			return err
		}
		log.Info("Re-issued ca-signed certificate")

	}

	if dsc.Status.Conditions.IsTrueFor(operatorv1alpha1.CertificateNeedsRenewalCondition) {
		// remove the condition
		patch := client.MergeFrom(dsc.DeepCopy())
		dsc.Status.Conditions.RemoveCondition(operatorv1alpha1.CertificateNeedsRenewalCondition)
		if err := r.Client.Status().Patch(ctx, dsc, patch); err != nil {
			return err
		}
	}

	if err := r.reconcileLabels(ctx, dsc, secret, issuerCert, log); err != nil {
		return err
	}

	return nil
}

func (r *DiscoveryServiceCertificateReconciler) reconcileLabels(ctx context.Context, dsc *operatorv1alpha1.DiscoveryServiceCertificate,
	secret *corev1.Secret, issuer *x509.Certificate, log logr.Logger) error {
	update := false

	// Label with the hash of the certificate
	if hash, ok := dsc.GetLabels()[operatorv1alpha1.CertificateHashLabelKey]; ok {
		if hash != common.Hash(secret.Data) {
			update = true
		}
	} else {
		update = true
	}

	// Label with the hash of the issuer certificate
	if hash, ok := dsc.GetLabels()[operatorv1alpha1.IssuerCertificateHashLabelKey]; ok {
		if hash != common.Hash(issuer) {
			update = true
		}
	} else {
		update = true
	}

	if update {
		patch := client.MergeFrom(dsc.DeepCopy())
		if dsc.GetLabels() == nil {
			dsc.SetLabels(map[string]string{})
		}
		dsc.ObjectMeta.Labels[operatorv1alpha1.CertificateHashLabelKey] = common.Hash(secret.Data)
		dsc.ObjectMeta.Labels[operatorv1alpha1.IssuerCertificateHashLabelKey] = common.Hash(issuer)
		if err := r.Client.Patch(ctx, dsc, patch); err != nil {
			return err
		}
		log.Info("Updated 'certificate-hash' label")
	}

	return nil
}

func (r *DiscoveryServiceCertificateReconciler) getCACertificate(ctx context.Context, cfg operatorv1alpha1.DiscoveryServiceCertificateSpec) (*x509.Certificate, interface{}, error) {

	s := &corev1.Secret{}
	err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      cfg.Signer.CASigned.SecretRef.Name,
			Namespace: cfg.Signer.CASigned.SecretRef.Namespace,
		},
		s,
	)

	if err != nil {
		return nil, nil, err
	}

	cert, err := pki.LoadX509Certificate(s.Data["tls.crt"])
	if err != nil {
		return nil, nil, err
	}

	key, err := pki.DecodePrivateKeyBytes(s.Data["tls.key"])
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

func genCASignedCertificateObject(cfg operatorv1alpha1.DiscoveryServiceCertificateSpec, issuerCert *x509.Certificate, issuerKey interface{}) (*corev1.Secret, error) {

	crt, key, err := pki.GenerateCertificate(
		issuerCert,
		issuerKey,
		cfg.CommonName,
		time.Duration(cfg.ValidFor)*time.Second,
		cfg.IsServerCertificate,
		cfg.IsCA,
		cfg.Hosts...,
	)
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.SecretRef.Name,
			Namespace: cfg.SecretRef.Namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": crt, "tls.key": key},
	}

	return secret, err
}
