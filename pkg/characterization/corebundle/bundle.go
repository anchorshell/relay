// Package corebundle loads the versioned characterization models embedded in
// public Relay release binaries. The repository intentionally contains no
// production artifact until a private training release has passed its gates.
package corebundle

import (
	"embed"
	"errors"
	"fmt"

	"github.com/anchorshell/relay/pkg/characterization"
)

var ErrUnavailable = errors.New("embedded core characterization bundle unavailable")

// bundle includes the release directory even before approved artifacts are
// copied into it. Model swaps therefore remain ordinary binary releases.
//
//go:embed models/core
var bundle embed.FS

func Load() (characterization.CandidateClassifier, error) {
	action, err := bundle.ReadFile("models/core/actions.asft")
	if err != nil {
		return nil, ErrUnavailable
	}
	metadata, err := bundle.ReadFile("models/core/metadata.asft")
	if err != nil {
		return nil, ErrUnavailable
	}
	classifier, err := characterization.NewFastTextClassifier(action, metadata)
	if err != nil {
		return nil, fmt.Errorf("load embedded core characterization bundle: %w", err)
	}
	return classifier, nil
}
