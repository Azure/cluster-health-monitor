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

// NewHealthyResult creates a new Result with StatusHealthy
func NewHealthyResult() Result {
	return Result{
		Status: StatusHealthy,
	}
}

// NewUnhealthyResult creates a new Result with StatusUnhealthy and error details
func NewUnhealthyResult(code, message string) Result {
	return Result{
		Status: StatusUnhealthy,
		ErrorDetail: &ErrorDetail{
			Code:    code,
			Message: message,
		},
	}
}

// NewUnknownResult creates a new Result with StatusUnknown and error details
func NewUnknownResult(code, message string) Result {
	return Result{
		Status: StatusUnknown,
		ErrorDetail: &ErrorDetail{
			Code:    code,
			Message: message,
		},
	}
}
