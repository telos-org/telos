package main

import (
	"time"

	"github.com/telos-org/telos-go/internal/cloud"
	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

func cloudSessionClientForRun(
	envID string,
	waitForEnvironment bool,
	readyTimeout time.Duration,
) (*cloud.Client, *cloud.Environment, error) {
	if envID != "" {
		return cloud.NewEnvironmentClient(envID)
	}
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, nil, err
	}
	env, err := control.CreateEnvironment()
	if err != nil {
		return nil, nil, err
	}
	if err := config.SaveEnvironmentAccessEntry(config.EnvironmentAccess{
		ID:    env.ID,
		Token: env.AccessToken,
	}); err != nil {
		return nil, nil, err
	}
	if waitForEnvironment {
		if readyTimeout <= 0 {
			readyTimeout = 15 * time.Minute
		}
		if err := cloud.WaitForEnvironment(env.Handle, readyTimeout); err != nil {
			return nil, nil, err
		}
	}
	return cloud.NewClient("https://"+env.Handle, env.AccessToken), env, nil
}

func listCloudSessions(envID string, limit int) ([]sessionapi.Session, error) {
	clients, err := cloudSessionClients(envID)
	if err != nil {
		return nil, err
	}
	var sessions []sessionapi.Session
	for _, client := range clients {
		found, err := client.ListSessions(limit)
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
	if envID != "" {
		client, _, err := cloud.NewEnvironmentClient(envID)
		if err != nil {
			return nil, err
		}
		return []*cloud.Client{client}, nil
	}

	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	envs, err := control.ListEnvironments()
	if err != nil {
		return nil, err
	}
	var clients []*cloud.Client
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
		clients = append(clients, cloud.NewClient("https://"+env.Handle, access.Token))
	}
	return clients, nil
}
