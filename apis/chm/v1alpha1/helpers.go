package v1alpha1

func (s *CheckNodeHealthStatus) UpdateCheckResult(rst CheckResult) {
	// Find existing result for this checker
	for i, result := range s.Results {
		if result.Name == rst.Name {
			// Update existing result
			s.Results[i] = rst
			return
		}
	}

	// Append new result if not found
	s.Results = append(s.Results, rst)
}
