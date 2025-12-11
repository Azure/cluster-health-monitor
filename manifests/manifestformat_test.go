package manifests

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	kyaml "sigs.k8s.io/kustomize/kyaml/yaml"
)

func Test_ValidateConfigMapFile(t *testing.T) {
	g := NewWithT(t)
	data, err := os.ReadFile("base/cluster-health-monitor/configmap.yaml")
	g.Expect(err).ToNot(HaveOccurred())

	var configMap corev1.ConfigMap
	err = yaml.Unmarshal(data, &configMap)
	g.Expect(err).ToNot(HaveOccurred())

	cfgStr, ok := configMap.Data["config.yaml"]
	g.Expect(ok).To(BeTrue())

	var cfg *config.Config
	cfg, err = config.ParseFromYAML([]byte(cfgStr))
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg).ToNot(BeNil())
}

func Test_ValidateConfigMapPatchFile(t *testing.T) {
	patchFiles := []string{
		"overlays/test/cluster-health-monitor/configmap.patch.yaml",
		"overlays/test-aks/configmap.patch.yaml",
	}

	for _, patchFile := range patchFiles {
		t.Run(patchFile, func(t *testing.T) {
			g := NewWithT(t)
			data, err := os.ReadFile(patchFile)
			g.Expect(err).ToNot(HaveOccurred())

			// convert data to string
			patch, err := kyaml.Parse(string(data)) // verify valid yaml
			g.Expect(err).ToNot(HaveOccurred())

			configPatch, err := patch.Pipe(kyaml.GetElementByIndex(1)) // Index 1 is the replace operation
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(configPatch).ToNot(BeNil())

			configPatchStr, err := configPatch.GetString("value") // get string value
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(configPatchStr).ToNot(BeEmpty())

			cfg, err := config.ParseFromYAML([]byte(configPatchStr)) // verify valid config
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(cfg).ToNot(BeNil())
		})
	}
}

func Test_ValidateYamlFormat_AllFiles(t *testing.T) {
	g := NewWithT(t)
	// list all yaml files in the directory
	paths, err := findYamlPaths(".")
	g.Expect(err).ToNot(HaveOccurred())

	for _, path := range paths {
		err := checkYamlFormat(path)
		g.Expect(err).ToNot(HaveOccurred())
	}
}

func findYamlPaths(root string) ([]string, error) {
	yamls := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Handle errors that occur during directory traversal
			return fmt.Errorf("error accessing path %q: %w", path, err)
		}

		// if this is yaml file, add to list
		if !d.IsDir() && (filepath.Ext(d.Name()) == ".yaml" || filepath.Ext(d.Name()) == ".yml") {
			yamls = append(yamls, path)
		}
		return nil // Continue traversal
	})

	if err != nil {
		return nil, err
	}
	return yamls, nil
}

func checkYamlFormat(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	_, err = kyaml.Parse(string(data))
	if err != nil {
		return err
	}
	return nil
}
