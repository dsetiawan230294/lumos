// Package cli wires up the Lumos cobra command tree.
package cli

import (
	"github.com/spf13/cobra"
)

// Execute builds the root command and runs it.
func Execute(version string) error {
	root := newRootCmd(version)
	return root.Execute()
}

func newRootCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "lumos",
		Short:         "Lumos — fast, lightweight mobile performance benchmarking",
		Long:          "Lumos runs performance benchmarks on Android and iOS devices with parallel, work-stealing execution and a Python automation bridge.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(
		newDevicesCmd(),
		newRunCmd(),
		newWatchCmd(),
		newReportCmd(),
		newCompareCmd(),
		newTrendsCmd(),
		newDoctorCmd(),
		newExportCmd(),
		newCheckCmd(),
	)

	return cmd
}
