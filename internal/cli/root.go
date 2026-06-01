package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/paultyng/ideate/internal/app"
	"github.com/paultyng/ideate/internal/version"
)

// flagNoGUI returns true when --no-gui was passed on this cmd or a parent.
// Persistent flags propagate, so any subcommand's Flags() carries it.
func flagNoGUI(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("no-gui")
	return v
}

// flagPreventSleep returns true when --prevent-sleep was passed on this
// cmd or a parent. Drives the in-app sleep-inhibitor toggle's initial
// state at startup; the user can still flip it from the footer at any
// time. Useful for long-running launches where the user wants the Mac
// to stay awake for active sessions without first reaching for the UI.
func flagPreventSleep(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("prevent-sleep")
	return v
}

var rootCmd = &cobra.Command{
	Use:           "ideate",
	Short:         "Idea lifecycle tracker",
	Version:       version.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if flagNoGUI(cmd) {
			return fmt.Errorf("no-gui mode: run `ideate` without --no-gui to start the app")
		}
		return app.Launch(app.LaunchConfig{
			View:         "dashboard",
			PreventSleep: flagPreventSleep(cmd),
		})
	},
}

func init() {
	rootCmd.PersistentFlags().Bool("no-gui", false, "prevent launching the GUI (headless mode for scripting)")
	rootCmd.PersistentFlags().Bool("prevent-sleep", false, "start with the sleep-inhibitor toggle ON; keeps the Mac awake while a session is actively working")
	rootCmd.AddCommand(reviewCmd)
	rootCmd.AddCommand(statusCmd)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
