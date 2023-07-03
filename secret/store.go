package secret

import (
	"context"
	"errors"
	"sync"

	"github.com/dagger/dagger/core"
	"github.com/moby/buildkit/session/secrets"
)

// ErrNotFound indicates a secret can not be found.
var ErrNotFound = errors.New("secret not found")

func NewStore() *Store {
	return &Store{
		secrets: map[string]string{},
	}
}

var _ secrets.SecretStore = &Store{}

type Store struct {
	gw        *core.GatewayClient
	sessionID string

	mu      sync.Mutex
	secrets map[string]string
}

func (store *Store) SetGatewayAndSession(gw *core.GatewayClient, sessionID string) {
	store.gw = gw
	store.sessionID = sessionID
}

// AddSecret adds the secret identified by user defined name with its plaintext
// value to the secret store.
func (store *Store) AddSecret(_ context.Context, name, plaintext string) (core.SecretID, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	secret := core.NewDynamicSecret(name)

	// add the plaintext to the map
	store.secrets[secret.Name] = plaintext

	return secret.ID()
}

// GetSecret returns the plaintext secret value.
//
// Its argument may either be the user defined name originally specified within
// a SecretID, or a full SecretID value.
//
// A user defined name will be received when secrets are used in a Dockerfile
// build.
//
// In all other cases, a SecretID is expected.
func (store *Store) GetSecret(ctx context.Context, idOrName string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	var name string
	if secret, err := core.SecretID(idOrName).ToSecret(); err == nil {
		if secret.IsOldFormat() {
			// use the legacy SecretID format
			return secret.LegacyPlaintext(ctx, store.gw, store.sessionID)
		}

		name = secret.Name
	} else {
		name = idOrName
	}

	plaintext, ok := store.secrets[name]
	if !ok {
		return nil, ErrNotFound
	}

	return []byte(plaintext), nil
}
