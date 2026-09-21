package relay

import (
	"context"
	"errors"
	"github.com/anchorshell/relay/internal/proxy"
	"github.com/anchorshell/relay/pkg/characterization"
)

func proxyCharacterizationEngineHook(hook func(context.Context, CharacterizationPolicyInput) (characterization.EngineID, error), partitioned bool) func(context.Context, string, proxy.TrustedRequestContext) (characterization.EngineID, error) {
	if hook == nil {
		if !partitioned {
			return nil
		}
		return func(context.Context, string, proxy.TrustedRequestContext) (characterization.EngineID, error) {
			return characterization.EngineAnchorShell, nil
		}
	}
	return func(ctx context.Context, id string, trusted proxy.TrustedRequestContext) (characterization.EngineID, error) {
		engine, err := hook(ctx, CharacterizationPolicyInput{RequestID: id, Metadata: cloneStringMap(trusted.Metadata)})
		if err != nil {
			return "", err
		}
		if !characterization.ValidEngine(engine) {
			return "", errors.New("invalid characterization engine selection")
		}
		return engine, nil
	}
}
