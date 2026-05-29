package cloud

import "context"

type RepoSetting struct {
	Repo     string `json:"repo"`
	IsPublic bool   `json:"isPublic"`
}

const getOrgRepoSettingsOperation = `
query GetOrgRepoSettings($org: String!) {
	org(name: $org) {
		repoSettings {
			repo
			isPublic
		}
	}
}
`

func (c *Client) OrgRepoSettings(ctx context.Context, orgName string) ([]RepoSetting, error) {
	var data struct {
		Org struct {
			RepoSettings []RepoSetting `json:"repoSettings"`
		} `json:"org"`
	}
	if err := c.doGraphQL(ctx, "GetOrgRepoSettings", getOrgRepoSettingsOperation, map[string]any{
		"org": orgName,
	}, &data); err != nil {
		return nil, err
	}
	return data.Org.RepoSettings, nil
}

const updateOrgRepoSettingOperation = `
mutation UpdateOrgRepoSetting($org: ID!, $repo: String!, $isPublic: Boolean!) {
	updateOrgRepoSetting(org: $org, setting: { repo: $repo, isPublic: $isPublic })
}
`

func (c *Client) UpdateOrgRepoSetting(ctx context.Context, orgID, repo string, isPublic bool) (bool, error) {
	var data struct {
		UpdateOrgRepoSetting bool `json:"updateOrgRepoSetting"`
	}
	if err := c.doGraphQL(ctx, "UpdateOrgRepoSetting", updateOrgRepoSettingOperation, map[string]any{
		"org":      orgID,
		"repo":     repo,
		"isPublic": isPublic,
	}, &data); err != nil {
		return false, err
	}
	return data.UpdateOrgRepoSetting, nil
}

const deleteOrgRepoSettingOperation = `
mutation DeleteOrgRepoSetting($org: ID!, $repo: String!) {
	deleteOrgRepoSetting(org: $org, repoName: $repo)
}
`

func (c *Client) DeleteOrgRepoSetting(ctx context.Context, orgID, repo string) (bool, error) {
	var data struct {
		DeleteOrgRepoSetting bool `json:"deleteOrgRepoSetting"`
	}
	if err := c.doGraphQL(ctx, "DeleteOrgRepoSetting", deleteOrgRepoSettingOperation, map[string]any{
		"org":  orgID,
		"repo": repo,
	}, &data); err != nil {
		return false, err
	}
	return data.DeleteOrgRepoSetting, nil
}
