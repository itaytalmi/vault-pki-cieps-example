// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package business

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/vault/sdk/helper/certutil"
)

var (
	shouldGenerateCA sync.Once
	caCertificate    *x509.Certificate
	caKey            crypto.Signer
)

func Evaluate(req *certutil.CIEPSRequest) (*certutil.CIEPSResponse, error) {
	shouldGenerateCA.Do(generateSelfSignedRoot)

	// Log the complete request payload
	log.Printf("Received CIEPS request: %+v", req)
	log.Printf("CSR Details:\n"+
		"  Subject: %+v\n"+
		"  DNSNames: %v\n"+
		"  EmailAddresses: %v\n"+
		"  IPAddresses: %v\n"+
		"  URIs: %v\n"+
		"  SignatureAlgorithm: %v",
		req.ParsedCSR.Subject,
		req.ParsedCSR.DNSNames,
		req.ParsedCSR.EmailAddresses,
		req.ParsedCSR.IPAddresses,
		req.ParsedCSR.URIs,
		req.ParsedCSR.SignatureAlgorithm)
	log.Printf("Vault Request Details:\n"+
		"  IssuerID: %v\n",
		req.VaultRequestKV.IssuerID)

	// Log incoming request details
	if commonName, ok := req.UserRequestKV["common_name"].(string); ok {
		log.Printf("Processing certificate request for CN: %q", commonName)
	}
	log.Printf("Request details - Organization: %v, Country: %v",
		req.ParsedCSR.Subject.Organization,
		req.ParsedCSR.Subject.Country)

	// Add domain validation
	if commonName, ok := req.UserRequestKV["common_name"].(string); ok && commonName != "" {
		log.Printf("Validating domain: %s", commonName)
		if strings.HasSuffix(commonName, ".unauthorized.cloudnativeapps.lab") {
			log.Printf("Request rejected: unauthorized domain %q", commonName)
			resp := &certutil.CIEPSResponse{
				Error:     fmt.Errorf("domain %q is not authorized", commonName).Error(),
				IssuerRef: req.VaultRequestKV.IssuerID,
			}
			return resp, nil
		}
		log.Printf("Domain validation successful for: %s", commonName)
	} else {
		log.Printf("Request rejected: Common Name is required")
		resp := &certutil.CIEPSResponse{
			Error:     fmt.Errorf("request rejected: Common Name is required").Error(),
			IssuerRef: req.VaultRequestKV.IssuerID,
		}
		return resp, nil
	}

	var err error

	csr := req.ParsedCSR
	cert := &x509.Certificate{}

	cert.RawSubjectPublicKeyInfo = csr.RawSubjectPublicKeyInfo
	cert.PublicKeyAlgorithm = csr.PublicKeyAlgorithm
	cert.PublicKey = csr.PublicKey

	cert.RawSubject = csr.RawSubject
	cert.Subject = csr.Subject

	// Add the common name from the request as a DNS name
	if commonName, ok := req.UserRequestKV["common_name"].(string); ok && commonName != "" {
		cert.DNSNames = []string{commonName}
	}

	cert.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageKeyAgreement
	cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}

	cert.NotBefore = time.Now().Add(-30 * time.Second)
	cert.NotAfter = time.Now().Add(10 * 24 * time.Hour)

	cert.BasicConstraintsValid = false
	cert.IsCA = false

	cert.SubjectKeyId, err = certutil.GetSubjectKeyID(cert.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to compute subjectKeyId: %w", err)
	}

	cert.SerialNumber = big.NewInt(2)

	extMsg := "CIEPS Demo Server Certificate"
	extValue, err := asn1.Marshal(extMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal certificate extension: %w", err)
	}

	cert.ExtraExtensions = []pkix.Extension{
		{
			Id:    asn1.ObjectIdentifier{2, 16, 840, 1, 113730, 1, 13},
			Value: extValue,
		},
	}

	// XXX: Go does not currently allow marshaling/creating a certificate
	// without signing it as is good API design. This signature by the
	// CIEPS service is discarded by Vault and doesn't impact the final
	// certificate (outside potentially of the SignatureAlgorithm being
	// used IF the CIEPS service's "fake CA" matches the key type of the
	// Vault-owned real CA.
	certBytes, err := x509.CreateCertificate(rand.Reader, cert, caCertificate, cert.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal certificate template: %w", err)
	}

	// Log certificate generation details
	log.Printf("Generated certificate for %q valid from %v to %v",
		cert.Subject.CommonName,
		cert.NotBefore,
		cert.NotAfter)

	resp := &certutil.CIEPSResponse{
		ParsedCertificate: &x509.Certificate{
			Raw: certBytes,
		},
		StoreCert: false,
		IssuerRef: req.VaultRequestKV.IssuerID,
	}

	resp.MarshalCertificate()
	log.Printf("Certificate request completed successfully for CN: %q", req.ParsedCSR.Subject.CommonName)

	return resp, nil
}

func generateSelfSignedRoot() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("failed to generate private key: %v", err))
	}
	pub := key.Public()

	caTemplate := &x509.Certificate{
		Subject:      pkix.Name{CommonName: "CIEPS Demo Root CA"},
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now(),
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	caBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, pub, key)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal CA certificate: %v", err))
	}

	ca, err := x509.ParseCertificate(caBytes)
	if err != nil {
		panic(fmt.Sprintf("failed to unmarshal CA certificate: %v", err))
	}

	caCertificate = ca
	caKey = key
}
