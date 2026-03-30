package podstartup

import "github.com/Azure/cluster-health-monitor/pkg/config"

const (
	testAzureDiskStorageClass = "managed-csi"
	testAzureFileStorageClass = "azurefile-csi"
	testAzureBlobStorageClass = "clusterhealthmonitor-azureblob-sc"
)

func csiConfigsFromTypes(types []config.CSIType) []config.CSIConfig {
	configs := make([]config.CSIConfig, 0, len(types))
	for _, t := range types {
		configs = append(configs, config.CSIConfig{
			Type:         t,
			StorageClass: defaultStorageClassForType(t),
		})
	}
	return configs
}

func defaultStorageClassForType(t config.CSIType) string {
	switch t {
	case config.CSITypeAzureDisk:
		return testAzureDiskStorageClass
	case config.CSITypeAzureFile:
		return testAzureFileStorageClass
	case config.CSITypeAzureBlob:
		return testAzureBlobStorageClass
	default:
		return ""
	}
}
