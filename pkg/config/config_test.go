package config

import (
	"os"
	"testing"

	. "github.com/onsi/gomega"
)

func TestParsefromYAML_ValidConfig(t *testing.T) {
	g := NewWithT(t)
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	defer os.Remove(tmpfile.Name())

	yamlContent := `
checkers:
  - name: dns-checker
    type: dns
    interval: 10s
    timeout: 5s
    dnsConfig:
      domain: example.com
  - name: pod-checker
    type: podStartup
    interval: 20s
    timeout: 10s
    podStartupConfig: {}
`
	_, err = tmpfile.WriteString(yamlContent)
	g.Expect(err).NotTo(HaveOccurred())
	tmpfile.Close()

	cfg, err := ParsefromYAML(tmpfile.Name())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).NotTo(BeNil())
	g.Expect(cfg.Checkers).To(HaveLen(2))
	g.Expect(cfg.Checkers[0].Name).To(Equal("dns-checker"))
	g.Expect(cfg.Checkers[0].Type).To(Equal(CheckTypeDNS))
	g.Expect(cfg.Checkers[0].DNSConfig).NotTo(BeNil())
	g.Expect(cfg.Checkers[0].DNSConfig.Domain).To(Equal("example.com"))
	g.Expect(cfg.Checkers[1].Name).To(Equal("pod-checker"))
	g.Expect(cfg.Checkers[1].Type).To(Equal(CheckTypePodStartup))
	g.Expect(cfg.Checkers[1].PodStartupConfig).NotTo(BeNil())
}

func TestParsefromYAML_DuplicateNames(t *testing.T) {
	g := NewWithT(t)
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	defer os.Remove(tmpfile.Name())

	yamlContent := `
checkers:
  - name: duplicate
    type: dns
    interval: 10s
    timeout: 5s
    dnsConfig:
      domain: example.com
  - name: duplicate
    type: podStartup
    interval: 20s
    timeout: 10s
    podStartupConfig: {}
`
	_, err = tmpfile.WriteString(yamlContent)
	g.Expect(err).NotTo(HaveOccurred())
	tmpfile.Close()

	cfg, err := ParsefromYAML(tmpfile.Name())
	g.Expect(err).To(HaveOccurred())
	g.Expect(cfg).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("duplicate checker names"))
}

func TestParsefromYAML_FileNotFound(t *testing.T) {
	g := NewWithT(t)
	cfg, err := ParsefromYAML("/nonexistent/file.yaml")
	g.Expect(err).To(HaveOccurred())
	g.Expect(cfg).To(BeNil())
}

func TestParsefromYAML_InvalidYAML(t *testing.T) {
	g := NewWithT(t)
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.WriteString("not: [valid, yaml")
	g.Expect(err).NotTo(HaveOccurred())
	tmpfile.Close()

	cfg, err := ParsefromYAML(tmpfile.Name())
	g.Expect(err).To(HaveOccurred())
	g.Expect(cfg).To(BeNil())
}
