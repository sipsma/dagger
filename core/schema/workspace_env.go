package schema

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/core/workspace"
	"github.com/dagger/dagger/dagql"
)

func (s *workspaceSchema) envList(
	ctx context.Context,
	parent *core.Workspace,
	args struct {
		All bool `default:"false"`
	},
) (dagql.Array[dagql.String], error) {
	var cfg *workspace.Config
	if parent.ConfigFile != "" {
		var err error
		cfg, err = readWorkspaceConfig(ctx, parent)
		if err != nil {
			return nil, err
		}
	}

	userCfg, _, _, err := readUserConfig(ctx)
	if err != nil {
		return nil, err
	}

	var names []string
	if args.All {
		names = workspace.EnvNamesAll(cfg, userCfg)
	} else {
		names = workspace.EnvNamesForWorkspace(cfg, userCfg, parent.EnvConfigKey)
	}
	out := make(dagql.Array[dagql.String], len(names))
	for i, name := range names {
		out[i] = dagql.String(name)
	}
	return out, nil
}

type workspaceEnvMutationArgs struct {
	Name   string
	Here   bool `default:"false"`
	Global bool `default:"false"`
}

func (s *workspaceSchema) envCreate(
	ctx context.Context,
	parent *core.Workspace,
	args workspaceEnvMutationArgs,
) (dagql.String, error) {
	if args.Name == "" {
		return "", fmt.Errorf("environment name is required")
	}
	if args.Global || (parent.ConfigFile == "" && parent.LocalConfigReadOnly()) {
		userCfg, existingData, _, err := readUserConfig(ctx)
		if err != nil {
			return "", err
		}
		if userCfg == nil {
			userCfg = &workspace.UserConfig{}
		}
		if workspace.EnsureUserEnv(userCfg, args.Name, "") {
			_ = existingData
			if err := writeUserConfigBytes(ctx, workspace.SerializeUserConfig(userCfg)); err != nil {
				return "", err
			}
		}
		return dagql.String(args.Name), nil
	}

	cfg, _, err := loadWorkspaceConfigForMutation(ctx, parent, workspaceConfigInitIfMissing, args.Here)
	if err != nil {
		return "", err
	}

	if workspace.EnsureEnv(cfg, args.Name) {
		if err := writeWorkspaceConfig(ctx, parent, cfg); err != nil {
			return "", err
		}
	}

	return dagql.String(args.Name), nil
}

func (s *workspaceSchema) envRemove(
	ctx context.Context,
	parent *core.Workspace,
	args workspaceEnvMutationArgs,
) (dagql.String, error) {
	if args.Name == "" {
		return "", fmt.Errorf("environment name is required")
	}
	if args.Global {
		userCfg, _, _, err := readUserConfig(ctx)
		if err != nil {
			return "", err
		}
		if err := workspace.RemoveUserEnv(userCfg, args.Name); err != nil {
			return "", err
		}
		if err := writeUserConfigBytes(ctx, workspace.SerializeUserConfig(userCfg)); err != nil {
			return "", err
		}
		return dagql.String(args.Name), nil
	}

	cfg, _, err := loadWorkspaceConfigForMutation(ctx, parent, workspaceConfigMustExist, args.Here)
	if err != nil {
		return "", err
	}

	if err := workspace.RemoveEnv(cfg, args.Name); err != nil {
		return "", err
	}
	if err := writeWorkspaceConfig(ctx, parent, cfg); err != nil {
		return "", err
	}

	return dagql.String(args.Name), nil
}
