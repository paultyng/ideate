//go:build !dev

package app

import "github.com/paultyng/ideate/internal/agent"

func registerTestAgentRunner(_ *agent.AgentCoordinator) {
	// No extra runners in release builds.
}
