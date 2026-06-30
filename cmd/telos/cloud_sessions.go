package main

import (
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

type cloudSessionTarget struct {
	client *cloud.Client
	env    cloud.Environment
}

func listCloudSessions(envID string, limit int, includeChildren bool) ([]sessionapi.Session, error) {
	targets, err := cloudSessionTargets(envID)
	if err != nil {
		return nil, err
	}
	var sessions []sessionapi.Session
	for _, target := range targets {
		found, err := target.client.ListSessions(limit, includeChildren)
		if err != nil {
			if envID != "" {
				return nil, err
			}
			continue
		}
		sessions = append(sessions, found...)
	}
	return sessions, nil
}

func cloudSessionClients(envID string) ([]*cloud.Client, error) {
	targets, err := cloudSessionTargets(envID)
	if err != nil {
		return nil, err
	}
	clients := make([]*cloud.Client, 0, len(targets))
	for _, target := range targets {
		clients = append(clients, target.client)
	}
	return clients, nil
}

func cloudSessionTargets(envID string) ([]cloudSessionTarget, error) {
	if envID != "" {
		client, env, err := cloud.NewEnvironmentClient(envID)
		if err != nil {
			return nil, err
		}
		return []cloudSessionTarget{{client: client, env: *env}}, nil
	}

	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	envs, err := control.ListEnvironments()
	if err != nil {
		return nil, err
	}
	var targets []cloudSessionTarget
	for _, env := range envs {
		if env.ID == "" || env.Handle == "" || env.State == "torn-down" {
			continue
		}
		access, ok := config.EnvironmentAccessByID(env.ID)
		if !ok {
			if !env.HasRecoverable {
				continue
			}
			recovered, err := control.IssueEnvironmentAccess(env.ID)
			if err != nil {
				continue
			}
			access = config.EnvironmentAccess{
				ID:    recovered.ID,
				Token: recovered.AccessToken,
			}
			if err := config.SaveEnvironmentAccessEntry(access); err != nil {
				return nil, err
			}
		}
		env.AccessToken = access.Token
		targets = append(targets, cloudSessionTarget{
			client: cloud.NewClient("https://"+env.Handle, access.Token),
			env:    env,
		})
	}
	return targets, nil
}
