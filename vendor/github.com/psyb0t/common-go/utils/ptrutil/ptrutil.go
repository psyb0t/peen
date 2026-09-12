package ptrutil

//nolint:gocheckcompilerdirectives // go:fix is valid go fix tooling
//go:fix inline
func Of[T any](v T) *T {
	return new(v)
}

func ZeroOrVal[T any](ptr *T) T { //nolint:ireturn
	var zero T

	if ptr != nil {
		return *ptr
	}

	return zero
}

func DefaultOrVal[T any](ptr *T, defaultVal T) T { //nolint:ireturn
	if ptr != nil {
		return *ptr
	}

	return defaultVal
}
