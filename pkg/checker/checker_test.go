package checker

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

type fakeChecker struct{ name string }

func (f *fakeChecker) Name() string                                   { return f.name }
func (f *fakeChecker) Run(ctx context.Context) (*types.Result, error) { return nil, nil }

func fakeBuilder(cfg *config.CheckerConfig) (Checker, error) {
	if cfg.Name == "fail" {
		return nil, errors.New("forced error")
	}
	return &fakeChecker{name: cfg.Name}, nil
}

func TestRegisterCheckerAndBuildChecker(t *testing.T) {
	testType := config.CheckerType("fake")
	RegisterChecker(testType, fakeBuilder)
	cfg := &config.CheckerConfig{Name: "foo", Type: testType}
	c, err := Build(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if c == nil {
		t.Fatal("expected checker, got nil")
	}
	if c.Name() != "foo" {
		t.Errorf("expected name 'foo', got %s", c.Name())
	}
}

func TestBuildCheckerUnknownType(t *testing.T) {
	cfg := &config.CheckerConfig{Name: "bar", Type: "unknown"}
	c, err := Build(cfg)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if c != nil {
		t.Errorf("expected nil checker, got %v", c)
	}
}

func TestBuildCheckerBuilderError(t *testing.T) {
	testType := config.CheckerType("fakeerr")
	RegisterChecker(testType, fakeBuilder)
	cfg := &config.CheckerConfig{Name: "fail", Type: testType}
	c, err := Build(cfg)
	if err == nil {
		t.Fatal("expected error from builder, got nil")
	}
	if c != nil {
		t.Errorf("expected nil checker, got %v", c)
	}
}
