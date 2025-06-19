package config

import (
	"testing"
	"time"
)

func TestConfigValidate_Valid(t *testing.T) {
	cfg := &Config{
		Checkers: []CheckerConfig{
			{
				Name:      "dns1",
				Type:      CheckTypeDNS,
				Interval:  10 * time.Second,
				Timeout:   2 * time.Second,
				DNSConfig: &DNSConfig{Domain: "example.com"},
			},
		},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestConfigValidate_NoCheckers(t *testing.T) {
	cfg := &Config{}
	err := cfg.validate()
	if err == nil || err.Error() != "at least one checker is required" {
		t.Errorf("expected error for no checkers, got %v", err)
	}
}

func TestConfigValidate_DuplicateNames(t *testing.T) {
	cfg := &Config{
		Checkers: []CheckerConfig{
			{Name: "foo", Type: CheckTypeDNS, Interval: 1, Timeout: 1, DNSConfig: &DNSConfig{Domain: "a"}},
			{Name: "foo", Type: CheckTypeDNS, Interval: 1, Timeout: 1, DNSConfig: &DNSConfig{Domain: "b"}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() == "" || !contains(err.Error(), "duplicate checker name") {
		t.Errorf("expected duplicate name error, got %v", err)
	}
}

func TestCheckerConfigValidate_MissingFields(t *testing.T) {
	chk := CheckerConfig{}
	err := chk.validate()
	if err == nil {
		t.Error("expected error for missing fields, got nil")
	}
	if !contains(err.Error(), "missing 'name'") || !contains(err.Error(), "missing 'type'") {
		t.Errorf("expected missing name/type error, got %v", err)
	}
}

func TestCheckerConfigValidate_UnsupportedType(t *testing.T) {
	chk := CheckerConfig{Name: "foo", Type: "badtype", Interval: 1, Timeout: 1}
	err := chk.validate()
	if err == nil || !contains(err.Error(), "unsupported type") {
		t.Errorf("expected unsupported type error, got %v", err)
	}
}

func contains(s, substr string) bool {
	return s != "" && substr != "" && (len(s) >= len(substr)) && (s == substr || (len(s) > len(substr) && (contains(s[1:], substr) || contains(s[:len(s)-1], substr))))
}
