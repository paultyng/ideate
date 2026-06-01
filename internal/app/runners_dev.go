//go:build dev

package app

import "github.com/paultyng/ideate/internal/agent"

func registerTestAgentRunner(coord *agent.AgentCoordinator) {
	coord.RegisterRunner("testagent", &agent.TestAgentRunner{})
}
