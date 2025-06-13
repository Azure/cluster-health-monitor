package types

// Status represents the health status of a checker
type Status string

const (
	// StatusHealthy indicates the checker passed
	StatusHealthy Status = "healthy"
	// StatusUnhealthy indicates the checker failed
	StatusUnhealthy Status = "unhealthy"
	// StatusUnknown indicates the checker status could not be determined
	StatusUnknown Status = "unknown"
)

// ErrorDetail contains error information when a check fails
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Result represents the result of running a health check
type Result struct {
	Status      Status       `json:"status"`
	ErrorDetail *ErrorDetail `json:"errorDetail,omitempty"`
}
