package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCommand(version string) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update Steward using the method it was installed with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(executable); err == nil {
				executable = resolved
			}
			env := update.Environment{GOOS: runtime.GOOS, Executable: executable, Owns: update.HostOwns, LookPath: exec.LookPath}
			source := update.Detect(env)
			command := update.Command(source, env)
			out := cmd.OutOrStdout()

			// Bound network access only; package managers may prompt for a password.
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			releases := update.Releases{Repository: os.Getenv("STEWARD_UPDATE_REPOSITORY")}
			latest, err := releases.Latest(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Current version: %s\nLatest version: %s\nInstalled with: %s\n", version, latest, source)
			if !update.Newer(latest, version) {
				fmt.Fprintln(out, "Steward is up to date.")
				return nil
			}
			if check {
				if command != nil {
					fmt.Fprintf(out, "Run `steward update` to run: %s\n", strings.Join(command, " "))
				} else {
					fmt.Fprintln(out, "Run `steward update` to install it.")
				}
				return nil
			}

			if command != nil {
				fmt.Fprintf(out, "Running: %s\n", strings.Join(command, " "))
				process := exec.CommandContext(cmd.Context(), command[0], command[1:]...)
				process.Stdin, process.Stdout, process.Stderr = os.Stdin, out, cmd.ErrOrStderr()
				if err := process.Run(); err != nil {
					return fmt.Errorf("%s failed: %w", command[0], err)
				}
			} else {
				staged, err := releases.Download(ctx, latest, runtime.GOOS, runtime.GOARCH, executable)
				if err != nil {
					if errors.Is(err, os.ErrPermission) {
						return fmt.Errorf("%w; rerun with permission to write %s", err, filepath.Dir(executable))
					}
					return err
				}
				if err := update.Replace(staged, executable, runtime.GOOS); err != nil {
					os.Remove(staged)
					return fmt.Errorf("replace %s: %w", executable, err)
				}
				fmt.Fprintf(out, "Updated Steward to %s at %s\n", latest, executable)
			}
			if running, err := serverRunning(); err == nil && running {
				fmt.Fprintln(out, "Restart the running server to use the new version: steward server stop, then steward server start.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check for a newer version")
	return cmd
}

func serverRunning() (bool, error) {
	path, err := resolveStatusPath("")
	if err != nil {
		return false, err
	}
	status, err := readServerStatus(path)
	if err != nil {
		return false, err
	}
	return processRunning(status.PID), nil
}
