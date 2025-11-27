package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
)

// waitForCheckerResultsMetricsValueIncrease is a helper function for the common pattern of waiting for metrics to increase. It gets initial
// metrics, then polls for metrics increases using Eventually with the provided timeout and interval. It is compatible with the
// cluster_health_monitor_pod_health_result_total and cluster_health_monitor_checker_result_total metrics. If errorCode is empty string, any error code is accepted.
func waitForCheckerResultsMetricsValueIncrease(localPort int, metricName string, checkerNames []string, checkerType, status, errorCode string, timeout, interval time.Duration, failureMessage string) {
	time0Metrics, err := getMetrics(localPort)
	Expect(err).NotTo(HaveOccurred())
	Eventually(func() bool {
		timeNMetrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())

		allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
			metricName, checkerNames, checkerType, status, errorCode,
		)
		Expect(err).NotTo(HaveOccurred())

		if !allIncreased {
			GinkgoWriter.Printf("Expected increase in %s results for checkers: %v, Actual: %v\n", status, checkerNames, increasedCheckers)
			return false
		}
		GinkgoWriter.Printf("Found increase in %s results for checkers %v\n", status, increasedCheckers)
		return true
	}, timeout, interval).Should(BeTrue(), failureMessage)
}

// verifyCheckerResultMetricsValueIncreased is a helper function to verify if metrics corresponding to checker results have increased from
// time0 to timeN. The function is compatible with the cluster_health_monitor_pod_health_result_total and cluster_health_monitor_checker_result_total
// metrics. For every provided checker name, it will check whether the metric with desired type, status, and error code has increased.
// Returns true only if there is an increase for every metric checked, false otherwise. It also returns a slice containing every checker name
// that increased (for logging/debug purposes), and an error if any comparison fails. If errorCode is empty string, any error code is accepted.
func verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics map[string]*dto.MetricFamily, metricName string, checkerNames []string, checkerType, status, errorCode string) (bool, []string, error) {
	var increasedCheckers []string

	// Check each checker name to see if its metric value has increased
	for _, checkerName := range checkerNames {
		labels := map[string]string{
			metricsCheckerNameLabel: checkerName,
			metricsCheckerTypeLabel: checkerType,
			metricsStatusLabel:      status,
		}
		if errorCode != "" {
			labels[metricsErrorCodeLabel] = errorCode
		}

		increased, err := compareCounterMetrics(time0Metrics, timeNMetrics, metricName, labels, func(value0, valueN float64) bool {
			return valueN > value0
		})

		if err != nil {
			return false, increasedCheckers, fmt.Errorf("failed to compare metrics for checker %s: %w", checkerName, err)
		}

		if increased {
			increasedCheckers = append(increasedCheckers, checkerName)
		}
	}

	// Return true only if all metrics increased
	allIncreased := len(increasedCheckers) == len(checkerNames)
	return allIncreased, increasedCheckers, nil
}

// compareCounterMetrics compares counter metrics between two metric maps based on specific labels and a comparison condition.
// It finds metrics with the specified labels and compares their values using the provided condition function.
// If a metric with the specified labels doesn't exist in either map, it uses 0 as the default value.
// Returns true if the condition is satisfied, false otherwise.
func compareCounterMetrics(time0Metrics, timeNMetrics map[string]*dto.MetricFamily, metricName string, labels map[string]string, condition func(float64, float64) bool) (bool, error) {
	value0, err := getCounterMetricValue(time0Metrics, metricName, labels)
	if err != nil {
		return false, fmt.Errorf("failed to get metric value from time0 metrics: %w", err)
	}

	valueN, err := getCounterMetricValue(timeNMetrics, metricName, labels)
	if err != nil {
		return false, fmt.Errorf("failed to get metric value from timeN metrics: %w", err)
	}

	return condition(value0, valueN), nil
}

// getCounterMetricValue retrieves the sum of all counter metric values with specific labels from a metric family map.
// If the metric or entries with the specific labels don't exist, it returns 0 as the default value. This helps when comparing metrics
// values over time because some metrics may not have been emitted yet.
func getCounterMetricValue(metrics map[string]*dto.MetricFamily, metricName string, labels map[string]string) (float64, error) {
	// Check if the metric family exists. If not, return 0 as default value.
	metricFamily, exists := metrics[metricName]
	if !exists {
		return 0, nil
	}

	// Sum all metrics with matching labels
	var totalValue float64
	for _, metric := range metricFamily.GetMetric() {
		if matchesLabels(metric, labels) {
			if counter := metric.GetCounter(); counter != nil {
				totalValue += counter.GetValue()
			} else {
				return 0, fmt.Errorf("metric %s exists but is not a counter", metricName)
			}
		}
	}

	return totalValue, nil
}

// matchesLabels checks if the targetLabels are a subset of a metric's labels.
func matchesLabels(metric *dto.Metric, targetLabels map[string]string) bool {
	// Create a map of the metric's labels for easier comparison
	metricLabels := make(map[string]string)
	for _, labelPair := range metric.GetLabel() {
		metricLabels[labelPair.GetName()] = labelPair.GetValue()
	}

	// Check if all target labels match
	for name, value := range targetLabels {
		if metricValue, exists := metricLabels[name]; !exists || metricValue != value {
			return false
		}
	}

	return true
}
