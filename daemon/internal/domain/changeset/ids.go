// Package changeset defines immutable Agent-originated project change proposals.
package changeset

import (
	"errors"
	"fmt"
	"strings"
)

const minimumRandomIDSuffixLength = 26

var (
	ErrInvalidChangeSetID    = errors.New("invalid ChangeSet ID")
	ErrInvalidOperationID    = errors.New("invalid ChangeSet Operation ID")
	ErrInvalidPreconditionID = errors.New("invalid ChangeSet Precondition ID")
	ErrInvalidConflictID     = errors.New("invalid ChangeSet Conflict ID")
)

type ChangeSetID string
type OperationID string
type PreconditionID string
type ConflictID string

func ParseChangeSetID(value string) (ChangeSetID, error) {
	if err := validateID(value, "cst_", ErrInvalidChangeSetID); err != nil {
		return "", err
	}
	return ChangeSetID(value), nil
}
func (id ChangeSetID) String() string  { return string(id) }
func (id ChangeSetID) Validate() error { return validateID(string(id), "cst_", ErrInvalidChangeSetID) }

func ParseOperationID(value string) (OperationID, error) {
	if err := validateID(value, "cop_", ErrInvalidOperationID); err != nil {
		return "", err
	}
	return OperationID(value), nil
}
func (id OperationID) String() string  { return string(id) }
func (id OperationID) Validate() error { return validateID(string(id), "cop_", ErrInvalidOperationID) }

func ParsePreconditionID(value string) (PreconditionID, error) {
	if err := validateID(value, "cpc_", ErrInvalidPreconditionID); err != nil {
		return "", err
	}
	return PreconditionID(value), nil
}
func (id PreconditionID) String() string { return string(id) }
func (id PreconditionID) Validate() error {
	return validateID(string(id), "cpc_", ErrInvalidPreconditionID)
}

func ParseConflictID(value string) (ConflictID, error) {
	if err := validateID(value, "ccf_", ErrInvalidConflictID); err != nil {
		return "", err
	}
	return ConflictID(value), nil
}
func (id ConflictID) String() string  { return string(id) }
func (id ConflictID) Validate() error { return validateID(string(id), "ccf_", ErrInvalidConflictID) }

func validateID(value, prefix string, category error) error {
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%w: expected prefix %q", category, prefix)
	}
	suffix := strings.TrimPrefix(value, prefix)
	if len(suffix) < minimumRandomIDSuffixLength {
		return fmt.Errorf("%w: random suffix is too short", category)
	}
	for _, character := range suffix {
		if !(character >= 'A' && character <= 'Z') && !(character >= '2' && character <= '7') {
			return fmt.Errorf("%w: suffix must use unpadded uppercase base32", category)
		}
	}
	return nil
}
