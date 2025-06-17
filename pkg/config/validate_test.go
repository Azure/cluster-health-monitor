package config

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestValidatePodStartupConfig(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *PodStartupConfig
		validateRes func(g *WithT, err error)
	}{
		{
			name: "valid config",
			cfg: &PodStartupConfig{
				Namespace:        "default",
				MaxSyntheticPods: 3,
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name: "nil config",
			cfg:  nil,
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("podStartupConfig is required"))
			},
		},
		{
			name: "missing namespace",
			cfg: &PodStartupConfig{
				Namespace:        "",
				MaxSyntheticPods: 3,
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("namespace is required"))
			},
		},
		{
			name: "invalid maxSyntheticPods",
			cfg: &PodStartupConfig{
				Namespace:        "default",
				MaxSyntheticPods: 0,
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("maxSyntheticPods must be greater than 0"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			err := tt.cfg.ValidatePodStartupConfig()
			tt.validateRes(g, err)
		})
	}
}
