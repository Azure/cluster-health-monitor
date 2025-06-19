package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
)

type fakeChecker struct {
	name     string
	runCount int32
	runErr   error
	delay    time.Duration
}

func (f *fakeChecker) Name() string { return f.name }
func (f *fakeChecker) Run(ctx context.Context) (*types.Result, error) {
	fmt.Println("Running fake checker:", f.name)
	atomic.AddInt32(&f.runCount, 1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return types.Healthy(), f.runErr
}
func (f *fakeChecker) Type() config.CheckerType { return config.CheckerType("fake") }

func TestScheduler_Run_Periodic(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	fakeChk := &fakeChecker{name: "periodic1", runErr: nil, delay: 0}
	chkSch := []CheckerSchedule{
		{
			Interval: 50 * time.Millisecond,
			Timeout:  0,
			Checker:  fakeChk,
		},
	}
	scheduler := NewScheduler(chkSch)
	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Millisecond)
	defer cancel()
	_ = scheduler.Start(ctx)
	g.Expect(fakeChk.runCount).To(BeNumerically(">=", 2))
}
