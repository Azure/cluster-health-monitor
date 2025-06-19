package config

import (
	"testing"
)

func TestParseFromYAML_Valid(t *testing.T) {
	yamlData := []byte(`
checkers:
  - name: dns1
    type: dns
    interval: 10s
    timeout: 2s
    dnsConfig:
      domain: example.com
`)
	cfg, err := ParseFromYAML(yamlData)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(cfg.Checkers) != 1 {
		t.Errorf("expected 1 checker, got %d", len(cfg.Checkers))
	}
	if cfg.Checkers[0].Name != "dns1" {
		t.Errorf("expected checker name dns1, got %s", cfg.Checkers[0].Name)
	}
}

func TestParseFromYAML_InvalidYAML(t *testing.T) {
	badYAML := []byte(`checkers: [name: dns1, type: dns`) // malformed
	_, err := ParseFromYAML(badYAML)
	if err == nil {
		t.Fatal("expected error for invalid yaml, got nil")
	}
}

func TestParseFromYAML_InvalidConfig(t *testing.T) {
	yamlData := []byte(`checkers: []`)
	_, err := ParseFromYAML(yamlData)
	if err == nil {
		t.Fatal("expected error for invalid config, got nil")
	}
}

func TestParseFromFile_NotExist(t *testing.T) {
	_, err := ParseFromFile("/tmp/does-not-exist.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
