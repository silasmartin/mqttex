// Package profiles persists broker connection profiles as a JSON file.
package profiles

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Profile struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Protocol      string   `json:"protocol"` // mqtt, mqtts, ws, wss
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Path          string   `json:"path,omitempty"` // websocket path
	Username      string   `json:"username,omitempty"`
	Password      string   `json:"password,omitempty"`
	ClientID      string   `json:"clientId,omitempty"`
	TLSInsecure   bool     `json:"tlsInsecure,omitempty"`
	CACert        string   `json:"caCert,omitempty"` // PEM; trusted instead of the system roots for mqtts and wss
	Subscriptions []string `json:"subscriptions"`
}

// Public is a profile as handed to the browser: the password never leaves the server.
type Public struct {
	Profile
	HasPassword bool       `json:"hasPassword"`
	CACerts     []CertInfo `json:"caCerts,omitempty"`
}

// CertInfo describes a certificate in words a person can check.
type CertInfo struct {
	Subject  string    `json:"subject"`
	NotAfter time.Time `json:"notAfter"`
}

func (p Profile) Public() Public {
	pub := Public{Profile: p, HasPassword: p.Password != ""}
	pub.Password = ""
	certs, _ := ParseCerts(p.CACert) // validated on save
	for _, c := range certs {
		subject := c.Subject.CommonName
		if subject == "" {
			subject = c.Subject.String()
		}
		pub.CACerts = append(pub.CACerts, CertInfo{Subject: subject, NotAfter: c.NotAfter})
	}
	return pub
}

func (p Profile) TLS() bool { return p.Protocol == "mqtts" || p.Protocol == "wss" }

// TLSConfig is nil for plain connections. With a CA certificate only that CA
// is trusted, like mosquitto's --cafile; without one the system roots are.
func (p Profile) TLSConfig() (*tls.Config, error) {
	if !p.TLS() {
		return nil, nil
	}
	cfg := &tls.Config{InsecureSkipVerify: p.TLSInsecure}
	if p.CACert != "" {
		certs, err := ParseCerts(p.CACert)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = x509.NewCertPool()
		for _, c := range certs {
			cfg.RootCAs.AddCert(c)
		}
	}
	return cfg, nil
}

// ParseCerts reads the certificates of a PEM text. Text around the blocks is
// skipped, as in "openssl s_client -showcerts" output. Any other block is
// refused, and so is anything that mentions a private key, even when it is
// indented or broken and therefore not a PEM block.
func ParseCerts(text string) ([]*x509.Certificate, error) {
	if strings.Contains(text, "PRIVATE KEY") {
		return nil, errors.New("CA certificate contains a private key; only the certificate belongs here, the key stays on the broker")
	}
	var certs []*x509.Certificate
	rest := []byte(text)
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("CA certificate: -----BEGIN %s----- is not a certificate", block.Type)
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CA certificate %d cannot be read: %w", len(certs)+1, err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 && strings.TrimSpace(text) != "" {
		return nil, errors.New("CA certificate contains no PEM certificate; it has to start with -----BEGIN CERTIFICATE-----")
	}
	return certs, nil
}

// URL is the broker address in the form autopaho expects.
func (p Profile) URL() string {
	u := fmt.Sprintf("%s://%s:%d", p.Protocol, p.Host, p.Port)
	if p.Protocol == "ws" || p.Protocol == "wss" {
		u += "/" + strings.TrimPrefix(p.Path, "/")
	}
	return u
}

func (p *Profile) normalize() error {
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	if p.Host == "" {
		return errors.New("host is required")
	}
	switch p.Protocol {
	case "mqtt", "mqtts", "ws", "wss":
	default:
		return fmt.Errorf("protocol %q is not one of mqtt, mqtts, ws, wss", p.Protocol)
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port %d is outside 1-65535", p.Port)
	}
	if p.Name == "" {
		p.Name = p.Host
	}
	// Only the certificates are stored, not the text around them. The field is
	// hidden for plain connections, so a CA left in it is not kept either.
	if !p.TLS() {
		p.CACert = ""
	}
	certs, err := ParseCerts(p.CACert)
	if err != nil {
		return err
	}
	var ca strings.Builder
	for _, c := range certs {
		ca.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	p.CACert = ca.String()
	subs := p.Subscriptions[:0:0]
	for _, s := range p.Subscriptions {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if err := ValidateFilter(s); err != nil {
			return err
		}
		subs = append(subs, s)
	}
	if len(subs) == 0 {
		subs = []string{"#"}
	}
	p.Subscriptions = subs
	return nil
}

// ValidateFilter checks an MQTT topic filter: "#" only as the last level,
// "+" and "#" only as a whole level.
func ValidateFilter(f string) error {
	if f == "" {
		return errors.New("topic filter is empty")
	}
	levels := strings.Split(f, "/")
	for i, l := range levels {
		switch {
		case l == "#":
			if i != len(levels)-1 {
				return fmt.Errorf("topic filter %q: # is only allowed as the last level", f)
			}
		case l == "+":
		case strings.ContainsAny(l, "#+"):
			return fmt.Errorf("topic filter %q: # and + must fill a whole level", f)
		}
	}
	return nil
}

// ValidateTopic checks a topic name used for publishing.
func ValidateTopic(t string) error {
	if t == "" {
		return errors.New("topic is empty")
	}
	if strings.ContainsAny(t, "#+") {
		return fmt.Errorf("topic %q contains a wildcard; publishing needs a concrete topic", t)
	}
	return nil
}

// Book is the set of saved profiles, backed by a file.
type Book struct {
	mu   sync.Mutex
	path string
	list []Profile
}

// DefaultPath is <user config dir>/mqttex/profiles.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mqttex", "profiles.json"), nil
}

func Open(path string) (*Book, error) {
	b := &Book{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &b.list); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return b, nil
}

func (b *Book) List() []Profile {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Profile(nil), b.list...)
}

func (b *Book) Get(id string) (Profile, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.list {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// Save creates the profile (empty ID) or updates it. On update an empty
// password keeps the stored one unless clearPassword is set.
func (b *Book) Save(p Profile, clearPassword bool) (Profile, error) {
	if err := p.normalize(); err != nil {
		return Profile{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	next := append([]Profile(nil), b.list...)
	if p.ID == "" {
		p.ID = newID()
		next = append(next, p)
	} else {
		i := indexOf(next, p.ID)
		if i < 0 {
			return Profile{}, fmt.Errorf("profile %q does not exist", p.ID)
		}
		if p.Password == "" && !clearPassword {
			p.Password = next[i].Password
		}
		next[i] = p
	}
	if err := b.write(next); err != nil {
		return Profile{}, err
	}
	b.list = next
	return p, nil
}

func (b *Book) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := indexOf(b.list, id)
	if i < 0 {
		return fmt.Errorf("profile %q does not exist", id)
	}
	next := append(append([]Profile(nil), b.list[:i]...), b.list[i+1:]...)
	if err := b.write(next); err != nil {
		return err
	}
	b.list = next
	return nil
}

// write replaces the file atomically. It holds passwords, so it is owner-only.
func (b *Book) write(list []Profile) error {
	if list == nil {
		list = []Profile{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

func indexOf(list []Profile, id string) int {
	for i, p := range list {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
