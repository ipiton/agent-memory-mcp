package memory

import "fmt"

// ErrNotFound is returned when a memory with the given ID does not exist.
type ErrNotFound struct {
	ID string
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("memory not found: %s", e.ID)
}

// ErrValidation is returned when input parameters fail validation.
type ErrValidation struct {
	Message string
}

func (e *ErrValidation) Error() string {
	return e.Message
}
