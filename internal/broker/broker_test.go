package broker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCAHint(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("failed to connect to mqtts://h:8883: %w", &tls.CertificateVerificationError{Err: err})
	}
	untrusted := wrap(errors.New(`x509: "h" certificate is not trusted`)) // how the macOS verifier says it

	if h := caHint(untrusted, false); !strings.Contains(h, "add its certificate") {
		t.Errorf("untrusted without ca: %q", h)
	}
	if h := caHint(wrap(x509.UnknownAuthorityError{}), true); !strings.Contains(h, "not signed by the CA certificate") {
		t.Errorf("unknown authority with ca: %q", h)
	}
	for name, err := range map[string]error{
		"wrong host":    wrap(x509.HostnameError{Certificate: &x509.Certificate{}, Host: "h"}),
		"expired":       wrap(x509.CertificateInvalidError{Reason: x509.Expired}),
		"refused":       errors.New("dial tcp 127.0.0.1:8883: connect: connection refused"),
		"tls, not x509": errors.New("tls: first record does not look like a TLS handshake"),
	} {
		if h := caHint(err, false); h != "" {
			t.Errorf("%s: hint %q", name, h)
		}
	}
}
