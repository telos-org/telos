package main

import (
	"encoding/json"
	"os"

	"github.com/telos-org/telos-go/internal/cloud"
)

func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

type environmentJSON struct {
	ID             string `json:"id"`
	Handle         string `json:"handle"`
	State          string `json:"state"`
	HasRecoverable bool   `json:"has_recoverable_access"`
}

func environmentOutput(env *cloud.Environment) *environmentJSON {
	if env == nil {
		return nil
	}
	out := environmentJSONFrom(env)
	return &out
}

func environmentJSONFrom(env *cloud.Environment) environmentJSON {
	return environmentJSON{
		ID:             env.ID,
		Handle:         env.Handle,
		State:          env.State,
		HasRecoverable: env.HasRecoverable,
	}
}

func environmentsOutput(envs []cloud.Environment) []environmentJSON {
	out := make([]environmentJSON, 0, len(envs))
	for i := range envs {
		out = append(out, environmentJSONFrom(&envs[i]))
	}
	return out
}
