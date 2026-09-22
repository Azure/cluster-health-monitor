package gpu

import (
	"testing"
	"time"
)

const h100SKU = "Standard_ND96isr_H100_v5"

func TestConfigWithDefaults(t *testing.T) {
	t.Parallel()

	got := Config{}.withDefaults()
	if got.ToolTimeout != defaultToolTimeout {
		t.Errorf("ToolTimeout = %v, want %v", got.ToolTimeout, defaultToolTimeout)
	}

	explicit := Config{ToolTimeout: time.Minute}.withDefaults()
	if explicit.ToolTimeout != time.Minute {
		t.Errorf("withDefaults() overwrote explicit values: %+v", explicit)
	}
}
