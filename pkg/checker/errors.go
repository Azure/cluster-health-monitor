package checker

import "errors"

var (
	// ErrSkipChecker signals that a checker should be skipped without causing application failure.
	// This can be used when a checker determines it's not applicable in the current environment.
	ErrSkipChecker = errors.New("skip checker")
)
