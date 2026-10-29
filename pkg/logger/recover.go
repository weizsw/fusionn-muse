package logger

import (
	"errors"
	"fmt"
	"runtime/debug"

	"go.uber.org/zap"
)

// PanicError is a recovered panic carried as an ordinary error, so the failure
// boundary that already records the operation's single ERROR also reports the
// panic. Error() holds only the panic value; the stack travels separately and
// renders through the bounded, redacted stack field (see PanicStack).
type PanicError struct {
	Value any
	Stack string
}

// Error renders the panic value only, never the stack.
func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }

// Recovered converts a recover() value into a *PanicError. Call it inside the
// deferred function, where the goroutine stack still holds the panicking
// frames:
//
//	defer func() {
//		if r := recover(); r != nil {
//			err = logger.Recovered(r)
//		}
//	}()
func Recovered(r any) error {
	return &PanicError{Value: r, Stack: string(debug.Stack())}
}

// PanicStack returns the stack field when err wraps a *PanicError and a no-op
// field otherwise, so a failure boundary can append it unconditionally.
func PanicStack(err error) zap.Field {
	var pe *PanicError
	if errors.As(err, &pe) && pe.Stack != "" {
		return zap.String("stack", pe.Stack)
	}
	return zap.Skip()
}
