package cog

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/plugin"
)

func doctorCmd(plugins []plugin.Plugin) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check external dependencies",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			results := p.Plugins.DoctorChecks(c.Context())
			return printDoctorResults(os.Stdout, results)
		},
	}
}

func printDoctorResults(w io.Writer, results map[string][]plugin.CheckResult) error {
	var warnings, errors int
	for id, checks := range results {
		fmt.Fprintf(w, "=== %s ===\n", id)
		for _, c := range checks {
			switch c.Status {
			case plugin.CheckOK:
				fmt.Fprintf(w, "  [OK]    %s\n", c.Name)
			case plugin.CheckWarn:
				fmt.Fprintf(w, "  [WARN]  %s: %s\n", c.Name, c.Message)
				warnings++
			case plugin.CheckError:
				fmt.Fprintf(w, "  [ERROR] %s: %s\n", c.Name, c.Message)
				errors++
			}
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "%d warning(s), %d error(s)\n", warnings, errors)

	if errors > 0 {
		return fmt.Errorf("doctor found %d error(s)", errors)
	}
	return nil
}
