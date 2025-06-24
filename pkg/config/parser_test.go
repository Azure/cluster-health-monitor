package config

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestParseFromYAML_Valid(t *testing.T) {
	g := NewWithT(t)
	yamlData := []byte(`
checkers:
  - name: dns1
    type: dns
    interval: 10s
    timeout: 2s
    dnsConfig:
      domain: example.com
`)
	cfg, err := parseFromYAML(yamlData)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg.Checkers).To(HaveLen(1))
	g.Expect(cfg.Checkers[0].Name).To(Equal("dns1"))
}

func TestParseFromYAML_InvalidYAML(t *testing.T) {
	g := NewWithT(t)
	badYAML := []byte(`checkers: [name: dns1, type: dns`)
	_, err := parseFromYAML(badYAML)
	g.Expect(err).To(HaveOccurred())
}

func TestParseFromYAML_InvalidConfig(t *testing.T) {
	g := NewWithT(t)
	yamlData := []byte(`checkers: []`)
	_, err := parseFromYAML(yamlData)
	g.Expect(err).To(HaveOccurred())
}

func TestParseFromFile_NotExist(t *testing.T) {
	g := NewWithT(t)
	_, err := ParseFromFile("/tmp/does-not-exist.yaml")
	g.Expect(err).To(HaveOccurred())
}
