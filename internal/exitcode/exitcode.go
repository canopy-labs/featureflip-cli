// Package exitcode defines the CLI's documented process exit codes and maps
// errors onto them.
package exitcode

import "errors"

const (
	OK         = 0 // success, including a flip that changed nothing
	Unexpected = 1 // network failure, server error after retries, bug
	Usage      = 2 // bad flags or arguments, missing --reason, ambiguous org/project
	Auth       = 3 // no token, 401, 403
	NotFound   = 4 // 404
	Rejected   = 5 // the API refused the change (400/409/412/422, plan limit)
	Confirm    = 6 // confirmation declined, or required without a terminal
	Drift      = 7 // generate types --check found a difference
)

// Coded is an error that knows which exit code it should produce.
type Coded interface {
	error
	ExitCode() int
}

// Classify returns the exit code for err: the code of the first Coded error
// in its chain, OK for nil, Unexpected otherwise.
func Classify(err error) int {
	if err == nil {
		return OK
	}
	var c Coded
	if errors.As(err, &c) {
		return c.ExitCode()
	}
	return Unexpected
}
