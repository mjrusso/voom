package forward

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ReadMutationReservations loads endpoints retained until confirmed backend termination.
func ReadMutationReservations(path string) ([]Decl, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var reservations []Decl
	if err := json.Unmarshal(data, &reservations); err != nil {
		return nil, fmt.Errorf("forward mutation at %s is malformed: %w", path, err)
	}
	return reservations, nil
}
