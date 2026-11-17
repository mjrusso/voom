package forward

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
)

// ManualMutation records the declaration change associated with one request.
type ManualMutation struct {
	Kind     MutationKind `json:"kind"`
	Endpoint Decl         `json:"endpoint"`
}

// MutationJournal retains request identities and reservations until reconciliation completes.
type MutationJournal struct {
	AutoForwards *[]RuntimeAuto    `json:"autoForwards,omitempty"`
	Manual       *ManualMutation   `json:"manual,omitempty"`
	Version      int               `json:"version"`
	Process      string            `json:"process"`
	Requests     []MutationRequest `json:"requests"`
	Reservations []Decl            `json:"-"`
}

// ReadMutationJournal accepts recovery journals and older reservation-only records.
// A nil journal means the file is absent; an empty legacy journal is still present.
func ReadMutationJournal(path string) (*MutationJournal, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal MutationJournal
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		err = json.Unmarshal(data, &journal.Reservations)
	} else {
		err = decodeMutationJSON(data, &journal)
		if err == nil {
			err = journal.validate()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("forward mutation at %s is malformed: %w", path, err)
	}
	for _, request := range journal.Requests {
		journal.Reservations = append(journal.Reservations, request.Endpoint)
	}
	if journal.AutoForwards != nil {
		for _, row := range *journal.AutoForwards {
			if row.Installed() && !slices.Contains(journal.Reservations, row.Endpoint()) {
				journal.Reservations = append(journal.Reservations, row.Endpoint())
			}
		}
	}
	return &journal, nil
}

func (j MutationJournal) validate() error {
	if j.Version != 1 || len(j.Requests) == 0 {
		return errors.New("invalid recovery journal")
	}
	fingerprint, err := hex.DecodeString(j.Process)
	if err != nil || len(fingerprint) != sha256.Size {
		return errors.New("invalid recovery process fingerprint")
	}
	ids := make(map[string]bool)
	for _, request := range j.Requests {
		if err := request.validate(); err != nil {
			return err
		}
		if request.Automatic() && j.AutoForwards == nil {
			return errors.New("automatic recovery requires a saved plan")
		}
		if ids[request.ID] {
			return errors.New("duplicate recovery request ID")
		}
		ids[request.ID] = true
	}
	if intent := j.Manual; intent != nil {
		if len(j.Requests) != 1 || j.AutoForwards != nil {
			return errors.New("manual intent requires one request and no automatic plan")
		}
		request := j.Requests[0]
		if (intent.Kind != ManualExpose && intent.Kind != ManualUnexpose) || request.Automatic() || request.Exposes() != (intent.Kind == ManualExpose) || request.Endpoint != intent.Endpoint {
			return errors.New("manual intent does not match its request")
		}
	}
	return nil
}

// ReadMutationReservations loads endpoints retained while a mutation is unresolved.
func ReadMutationReservations(path string) ([]Decl, error) {
	journal, err := ReadMutationJournal(path)
	if err != nil || journal == nil {
		return nil, err
	}
	return journal.Reservations, nil
}

func decodeMutationJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if len(bytes.TrimSpace(data[decoder.InputOffset():])) != 0 {
		return errors.New("trailing journal data")
	}
	return nil
}
