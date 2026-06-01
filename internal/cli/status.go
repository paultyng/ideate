package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/paultyng/ideate/internal/ipc"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if Ideate is running",
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := ipc.NewClient()
		if err != nil {
			return err
		}

		resp, err := client.GetStatus(cmd.Context())
		if err != nil {
			return fmt.Errorf("ideate is not running")
		}

		sleep := "off"
		if resp.GetSleepEnabled() {
			if resp.GetSleepHeld() {
				sleep = "on (active — Mac is being kept awake)"
			} else {
				sleep = "on (idle — no busy session, OS may sleep)"
			}
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"Ideate is running (version %s, uptime %s, prevent-sleep %s)\n",
			resp.GetVersion(), resp.GetUptime(), sleep)
		return nil
	},
}
