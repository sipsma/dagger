package schema

import (
	"github.com/dagger/dagger/core"
)

type secretSchema struct {
	*MergedSchemas
}

var _ ExecutableSchema = &secretSchema{}

func (s *secretSchema) Name() string {
	return "secret"
}

func (s *secretSchema) Schema() string {
	return Secret
}

var secretIDResolver = stringResolver(core.SecretID(""))

func (s *secretSchema) Resolvers() Resolvers {
	return Resolvers{
		"SecretID": secretIDResolver,
		"Query": ObjectResolver{
			"secret":    ToResolver(s.secret),
			"setSecret": ToResolver(s.setSecret),
		},
		"Secret": ObjectResolver{
			"id":        ToResolver(s.id),
			"plaintext": ToResolver(s.plaintext),
		},
	}
}

func (s *secretSchema) Dependencies() []ExecutableSchema {
	return nil
}

func (s *secretSchema) id(ctx *core.Context, parent *core.Secret, args any) (core.SecretID, error) {
	return parent.ID()
}

type secretArgs struct {
	ID core.SecretID
}

func (s *secretSchema) secret(ctx *core.Context, parent any, args secretArgs) (*core.Secret, error) {
	return args.ID.ToSecret()
}

type SecretPlaintext string

// This method ensures that the progrock vertex info does not display the plaintext.
func (s SecretPlaintext) MarshalText() ([]byte, error) {
	return []byte("***"), nil
}

type setSecretArgs struct {
	Name      string
	Plaintext SecretPlaintext
}

<<<<<<< HEAD
func (s *secretSchema) setSecret(ctx *router.Context, parent any, args setSecretArgs) (*core.Secret, error) {
	secretID, err := s.secrets.AddSecret(ctx, args.Name, []byte(args.Plaintext))
=======
func (s *secretSchema) setSecret(ctx *core.Context, parent any, args setSecretArgs) (*core.Secret, error) {
	secretID, err := s.secrets.AddSecret(ctx, args.Name, string(args.Plaintext))
>>>>>>> 26ff4433 (WIP session frontend, basics + local import/export working.)
	if err != nil {
		return nil, err
	}

	return secretID.ToSecret()
}

<<<<<<< HEAD
func (s *secretSchema) plaintext(ctx *router.Context, parent *core.Secret, args any) (string, error) {
=======
func (s *secretSchema) plaintext(ctx *core.Context, parent *core.Secret, args any) (string, error) {
	if parent.IsOldFormat() {
		bytes, err := parent.LegacyPlaintext(ctx, s.bk)
		return string(bytes), err
	}

>>>>>>> 26ff4433 (WIP session frontend, basics + local import/export working.)
	id, err := parent.ID()
	if err != nil {
		return "", err
	}

	bytes, err := s.secrets.GetSecret(ctx, id.String())
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}
