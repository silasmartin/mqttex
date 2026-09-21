package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
