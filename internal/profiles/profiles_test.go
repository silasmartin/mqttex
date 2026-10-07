package profiles

import (
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/silasmartin/mqttex/internal/testcert"
)

func TestValidateFilter(t *testing.T) {
	valid := []string{"#", "/topic/#", "+", "a/+/b", "/topic/+/status", "*", "a//b", "$SYS/#"}
	invalid := []string{"", "a/#/b", "a#", "#/a", "a/b+", "+a/b", "a/#b"}
	for _, f := range valid {
		if err := ValidateFilter(f); err != nil {
			t.Errorf("ValidateFilter(%q) = %v, want nil", f, err)
		}
	}
	for _, f := range invalid {
		if err := ValidateFilter(f); err == nil {
			t.Errorf("ValidateFilter(%q) = nil, want error", f)
		}
	}
}

func TestValidateTopic(t *testing.T) {
	if err := ValidateTopic("/topic/SN1/cmd"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	for _, topic := range []string{"", "a/#", "a/+/b"} {
		if err := ValidateTopic(topic); err == nil {
			t.Errorf("ValidateTopic(%q) = nil, want error", topic)
		}
	}
}

func TestSaveUpdateDeleteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "profiles.json")
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	p, err := b.Save(Profile{Protocol: "mqtts", Host: " broker.example ", Port: 8883, Username: "u", Password: "secret",
		Subscriptions: []string{" /topic/# ", ""}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Name != "broker.example" || p.Host != "broker.example" || len(p.Subscriptions) != 1 || p.Subscriptions[0] != "/topic/#" {
		t.Fatalf("saved profile = %+v", p)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("profiles file mode = %v, want 0600", info.Mode().Perm())
	}

	// An update without a password keeps the stored one.
	p.Password = ""
	p.Name = "prod"
	if _, err := b.Save(p, false); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(p.ID)
	if !ok || got.Password != "secret" || got.Name != "prod" {
		t.Fatalf("reloaded profile = %+v", got)
	}

	if _, err := b.Save(p, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = b.Get(p.ID); got.Password != "" {
		t.Fatalf("password was not cleared: %+v", got)
	}

	if err := b.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(p.ID); err == nil {
		t.Fatal("deleting a missing profile must fail")
	}
	if len(b.List()) != 0 {
		t.Fatalf("list = %+v", b.List())
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	b, _ := Open(filepath.Join(t.TempDir(), "p.json"))
	bad := []Profile{
		{Protocol: "mqtt", Port: 1883},
		{Protocol: "http", Host: "h", Port: 1883},
		{Protocol: "mqtt", Host: "h", Port: 0},
		{Protocol: "mqtt", Host: "h", Port: 1883, Subscriptions: []string{"a/#/b"}},
		{ID: "missing", Protocol: "mqtt", Host: "h", Port: 1883},
	}
	for _, p := range bad {
		if _, err := b.Save(p, false); err == nil {
			t.Errorf("Save(%+v) = nil error", p)
		}
	}
}

func TestDefaultSubscriptionAndURL(t *testing.T) {
	b, _ := Open(filepath.Join(t.TempDir(), "p.json"))
	p, err := b.Save(Profile{Protocol: "wss", Host: "h", Port: 443, Path: "mqtt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Subscriptions) != 1 || p.Subscriptions[0] != "#" {
		t.Errorf("subscriptions = %v, want [#]", p.Subscriptions)
	}
	if p.URL() != "wss://h:443/mqtt" {
		t.Errorf("url = %s", p.URL())
	}
	if u := (Profile{Protocol: "mqtt", Host: "h", Port: 1883, Path: "/x"}).URL(); u != "mqtt://h:1883" {
		t.Errorf("url = %s", u)
	}
}

func TestPublicNeverContainsThePassword(t *testing.T) {
	data, err := json.Marshal(Profile{ID: "1", Host: "h", Password: "hunter2"}.Public())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") || !strings.Contains(string(data), `"hasPassword":true`) {
		t.Fatalf("public json = %s", data)
	}
}

func TestCACertIsValidatedAndSummarized(t *testing.T) {
	expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	ca := testcert.NewCA(t, "Test Root CA", expiry)
	other := testcert.NewCA(t, "Other CA", expiry)
	b, _ := Open(filepath.Join(t.TempDir(), "p.json"))

	// Text around the blocks, as openssl prints it, is fine.
	bundle := "subject=CN = Test Root CA\n" + ca.PEM + "---\n" + other.PEM + "\nServer certificate ends here\n"
	p, err := b.Save(Profile{Protocol: "mqtts", Host: "h", Port: 8883, CACert: "  " + bundle + "\n\n"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.CACert != strings.TrimSpace(bundle) {
		t.Errorf("stored ca = %q", p.CACert)
	}
	pub := p.Public()
	if len(pub.CACerts) != 2 || pub.CACerts[0].Subject != "Test Root CA" || !pub.CACerts[0].NotAfter.Equal(expiry) || pub.CACerts[1].Subject != "Other CA" {
		t.Fatalf("summary = %+v", pub.CACerts)
	}
	data, _ := json.Marshal(pub)
	if !strings.Contains(string(data), `"caCert":"subject=CN`) || !strings.Contains(string(data), `"notAfter":"2030-01-02T03:04:05Z"`) {
		t.Errorf("public json = %s", data)
	}

	// Unlike the password, an empty field removes the stored certificate.
	p.CACert = ""
	if _, err := b.Save(p, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Get(p.ID); got.CACert != "" || got.Public().CACerts != nil {
		t.Errorf("ca was not removed: %+v", got)
	}

	bad := map[string]struct{ text, want string }{
		"not pem":     {"hello", "no PEM certificate"},
		"der as text": {string(ca.DER), "no PEM certificate"},
		"private key": {ca.PEM + "-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n", "contains a private key"},
		"other block": {"-----BEGIN X509 CRL-----\nAAAA\n-----END X509 CRL-----\n", "-----BEGIN X509 CRL----- is not a certificate"},
		"broken cert": {"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", "CA certificate 1 cannot be read"},
	}
	for name, c := range bad {
		if _, err := b.Save(Profile{Protocol: "mqtts", Host: "h", Port: 8883, CACert: c.text}, false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if err := (&Profile{Protocol: "mqtts", Host: "h", Port: 8883, CACert: " \n"}).normalize(); err != nil {
		t.Errorf("blank ca: %v", err)
	}
}

func TestTLSConfigTrustsOnlyTheCACert(t *testing.T) {
	ca := testcert.NewCA(t, "Test Root CA", time.Now().Add(time.Hour))
	leaf, err := x509.ParseCertificate(ca.ServerTLS(t).Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	for _, proto := range []string{"mqtt", "ws"} {
		if cfg, err := (Profile{Protocol: proto, CACert: ca.PEM}).TLSConfig(); cfg != nil || err != nil {
			t.Errorf("%s: cfg = %v err = %v, want no TLS", proto, cfg, err)
		}
	}

	system, err := (Profile{Protocol: "wss", TLSInsecure: true}).TLSConfig()
	if err != nil || system == nil || system.RootCAs != nil || !system.InsecureSkipVerify {
		t.Fatalf("without ca: %+v %v", system, err)
	}

	cfg, err := (Profile{Protocol: "mqtts", CACert: ca.PEM}).TLSConfig()
	if err != nil || cfg.RootCAs == nil || cfg.InsecureSkipVerify {
		t.Fatalf("with ca: %+v %v", cfg, err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: cfg.RootCAs}); err != nil {
		t.Errorf("a certificate signed by the CA is not trusted: %v", err)
	}
	other := testcert.NewCA(t, "Other CA", time.Now().Add(time.Hour))
	cfg, _ = (Profile{Protocol: "mqtts", CACert: other.PEM}).TLSConfig()
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: cfg.RootCAs}); err == nil {
		t.Error("a certificate from another CA is trusted")
	}

	if _, err := (Profile{Protocol: "mqtts", CACert: "hello"}).TLSConfig(); err == nil {
		t.Error("a broken stored CA certificate must fail")
	}
}
