package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func datasourceSettings(t *testing.T, jsonData map[string]interface{}, secure map[string]string) backend.DataSourceInstanceSettings {
	t.Helper()
	raw, err := json.Marshal(jsonData)
	if err != nil {
		t.Fatal(err)
	}
	return backend.DataSourceInstanceSettings{
		JSONData:                raw,
		DecryptedSecureJSONData: secure,
	}
}

func localCertificatePair(t *testing.T, serial int64) ([]byte, []byte, *x509.Certificate) {
	return localCertificatePairWithProperties(
		t,
		serial,
		true,
		x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment|x509.KeyUsageCertSign,
	)
}

func localCertificatePairWithProperties(t *testing.T, serial int64, isCA bool, keyUsage x509.KeyUsage) ([]byte, []byte, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "asyncq plugin test root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"localhost"},
		KeyUsage:              keyUsage,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		parsed
}

func validDatasourceJSON() map[string]interface{} {
	return map[string]interface{}{
		"host":    "localhost",
		"port":    5000,
		"timeout": "5000",
	}
}

func TestNewKdbDatasourceRejectsInvalidEndpointAndTimeout(t *testing.T) {
	tests := []struct {
		name string
		data map[string]interface{}
	}{
		{name: "empty host", data: map[string]interface{}{"host": "", "port": 5000, "timeout": "5000"}},
		{name: "invalid host", data: map[string]interface{}{"host": "bad host", "port": 5000, "timeout": "5000"}},
		{name: "zero port", data: map[string]interface{}{"host": "localhost", "port": 0, "timeout": "5000"}},
		{name: "large port", data: map[string]interface{}{"host": "localhost", "port": 65536, "timeout": "5000"}},
		{name: "zero timeout", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": "0"}},
		{name: "negative timeout", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": "-1"}},
		{name: "duration syntax", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": "5s"}},
		{name: "numeric with whitespace", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": " 5000 "}},
		{name: "too large timeout", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": "300001"}},
		{name: "overflow timeout", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": "999999999999999999999999"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewKdbDatasource(context.Background(), datasourceSettings(t, test.data, nil)); err == nil {
				t.Fatal("invalid datasource settings were accepted")
			}
		})
	}
}

func TestNewKdbDatasourceBlankOrMissingTimeoutUsesBoundedCompatibilityDefault(t *testing.T) {
	tests := []struct {
		name string
		data map[string]interface{}
	}{
		{name: "missing", data: map[string]interface{}{"host": "localhost", "port": 5000}},
		{name: "blank", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": ""}},
		{name: "whitespace only", data: map[string]interface{}{"host": "localhost", "port": 5000, "timeout": " \t "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, err := NewKdbDatasource(context.Background(), datasourceSettings(t, test.data, nil))
			if err != nil {
				t.Fatalf("blank/missing timeout rejected: %v", err)
			}
			ds := instance.(*KdbDatasource)
			t.Cleanup(ds.Dispose)
			if ds.DialTimeout != time.Second {
				t.Fatalf("compatibility timeout = %v, want 1s", ds.DialTimeout)
			}
		})
	}
}

func TestNewKdbDatasourceRejectsUnrepresentableAuthentication(t *testing.T) {
	tests := []struct {
		name   string
		secure map[string]string
	}{
		{name: "username separator", secure: map[string]string{"username": "bad:user"}},
		{name: "username NUL", secure: map[string]string{"username": "bad\x00user"}},
		{name: "password NUL", secure: map[string]string{"password": "bad\x00password"}},
		{name: "combined authentication too long", secure: map[string]string{"password": strings.Repeat("x", 253)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewKdbDatasource(context.Background(), datasourceSettings(t, validDatasourceJSON(), test.secure)); err == nil {
				t.Fatal("unrepresentable authentication was accepted")
			}
		})
	}

	instance, err := NewKdbDatasource(context.Background(), datasourceSettings(t, validDatasourceJSON(), map[string]string{
		"password": strings.Repeat("x", 252),
	}))
	if err != nil {
		t.Fatalf("exact 253-byte combined authentication was rejected: %v", err)
	}
	t.Cleanup(instance.(*KdbDatasource).Dispose)
}

func TestNewKdbDatasourceTLSFailsClosed(t *testing.T) {
	cert, key, _ := localCertificatePair(t, 11)
	_, otherKey, _ := localCertificatePair(t, 12)
	leaf, _, _ := localCertificatePairWithProperties(
		t,
		13,
		false,
		x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment,
	)
	caWithoutCertSign, _, _ := localCertificatePairWithProperties(
		t,
		14,
		true,
		x509.KeyUsageDigitalSignature,
	)
	base := func() map[string]interface{} {
		data := validDatasourceJSON()
		data["withTLS"] = true
		return data
	}
	tests := []struct {
		name   string
		data   map[string]interface{}
		secure map[string]string
	}{
		{name: "missing pair", data: base()},
		{name: "missing key", data: base(), secure: map[string]string{"tlsCertificate": string(cert)}},
		{name: "missing certificate", data: base(), secure: map[string]string{"tlsKey": string(key)}},
		{name: "bad certificate", data: base(), secure: map[string]string{"tlsCertificate": "not PEM", "tlsKey": string(key)}},
		{name: "mismatched pair", data: base(), secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(otherKey)}},
		{name: "oversized certificate", data: base(), secure: map[string]string{"tlsCertificate": strings.Repeat("x", maxTLSCertificateBytes+1), "tlsKey": string(key)}},
		{name: "oversized key", data: base(), secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": strings.Repeat("x", maxTLSPrivateKeyBytes+1)}},
		{
			name: "selected CA missing",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key)},
		},
		{
			name: "selected CA malformed despite skip verify",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				data["skipVerifyTLS"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": "not PEM"},
		},
		{
			name: "selected CA trailing garbage",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": string(cert) + "\nnot PEM"},
		},
		{
			name: "selected CA preamble garbage",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": "not PEM\n" + string(cert)},
		},
		{
			name: "selected CA inter-block garbage",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": string(cert) + "\nnot PEM\n" + string(cert)},
		},
		{
			name: "selected CA is a leaf",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": string(leaf)},
		},
		{
			name: "selected CA cannot sign certificates",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": string(caWithoutCertSign)},
		},
		{
			name: "selected CA bundle oversized",
			data: func() map[string]interface{} {
				data := base()
				data["withCACert"] = true
				return data
			}(),
			secure: map[string]string{"tlsCertificate": string(cert), "tlsKey": string(key), "caCert": strings.Repeat("x", maxTLSCABundleBytes+1)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewKdbDatasource(context.Background(), datasourceSettings(t, test.data, test.secure)); err == nil {
				t.Fatal("invalid TLS settings were accepted")
			}
		})
	}
}

func TestNewKdbDatasourceBuildsVerifiedTLSConfigWithSystemAndCustomRoots(t *testing.T) {
	cert, key, parsed := localCertificatePair(t, 21)
	data := validDatasourceJSON()
	data["withTLS"] = true
	data["withCACert"] = true
	instance, err := NewKdbDatasource(context.Background(), datasourceSettings(t, data, map[string]string{
		"tlsCertificate": string(cert),
		"tlsKey":         string(key),
		"caCert":         string(cert),
	}))
	if err != nil {
		t.Fatalf("valid TLS datasource failed: %v", err)
	}
	ds := instance.(*KdbDatasource)
	t.Cleanup(ds.Dispose)
	config := ds.TlsServerConfig
	if config == nil {
		t.Fatal("TLS config was not created")
	}
	if config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("minimum TLS version is %x", config.MinVersion)
	}
	if config.ServerName != "" {
		t.Fatalf("TLS ServerName should be derived by the normalized transport dialer: %q", config.ServerName)
	}
	if config.InsecureSkipVerify {
		t.Fatal("server verification was unexpectedly disabled")
	}
	if len(config.Certificates) != 1 || config.RootCAs == nil {
		t.Fatalf("client pair/custom roots missing: certs=%d roots=%v", len(config.Certificates), config.RootCAs)
	}
	foundCustom := false
	for _, subject := range config.RootCAs.Subjects() {
		if bytes.Equal(subject, parsed.RawSubject) {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Fatal("custom CA was not appended to root pool")
	}
	if system, systemErr := x509.SystemCertPool(); systemErr == nil && system != nil {
		if len(config.RootCAs.Subjects()) < len(system.Subjects())+1 {
			t.Fatalf("custom pool appears to have replaced system roots: system=%d configured=%d", len(system.Subjects()), len(config.RootCAs.Subjects()))
		}
	}
}

func TestNewKdbDatasourceDelegatesScopedIPv6CertificateNameToTransport(t *testing.T) {
	cert, key, _ := localCertificatePair(t, 22)
	data := validDatasourceJSON()
	data["host"] = "[fe80::1%eth0]"
	data["withTLS"] = true
	instance, err := NewKdbDatasource(context.Background(), datasourceSettings(t, data, map[string]string{
		"tlsCertificate": string(cert),
		"tlsKey":         string(key),
	}))
	if err != nil {
		t.Fatalf("scoped IPv6 TLS datasource failed: %v", err)
	}
	ds := instance.(*KdbDatasource)
	t.Cleanup(ds.Dispose)
	if ds.Host != "fe80::1%eth0" {
		t.Fatalf("normalized scoped host = %q", ds.Host)
	}
	if ds.TlsServerConfig == nil || ds.TlsServerConfig.ServerName != "" {
		t.Fatalf("scoped TLS ServerName must be left for the transport to normalize: %#v", ds.TlsServerConfig)
	}
}

func TestAppendValidatedCACertificatesRejectsNonCertificatePEM(t *testing.T) {
	pool := x509.NewCertPool()
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}})
	if err := appendValidatedCACertificates(pool, keyBlock); err == nil {
		t.Fatal("non-certificate PEM was accepted as a CA")
	}
}

func TestAppendValidatedCACertificatesAcceptsCAWithoutKeyUsageExtension(t *testing.T) {
	certificate, _, parsed := localCertificatePairWithProperties(t, 31, true, 0)
	if parsed.KeyUsage != 0 {
		t.Fatalf("test certificate unexpectedly has key usage %v", parsed.KeyUsage)
	}
	pool := x509.NewCertPool()
	if err := appendValidatedCACertificates(pool, certificate); err != nil {
		t.Fatalf("CA without KeyUsage extension was rejected: %v", err)
	}
	found := false
	for _, subject := range pool.Subjects() {
		if bytes.Equal(subject, parsed.RawSubject) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("accepted CA was not added to the pool")
	}
}
