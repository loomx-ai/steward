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
		Short: tr("Update Steward using the method it was installed with", "按安装方式更新 Steward"),
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
			fmt.Fprintf(out, tr("Current version: %s\nLatest version: %s\nInstalled with: %s\n", "当前版本：%s\n最新版本：%s\n安装方式：%s\n"), version, latest, source)
			if !update.Newer(latest, version) {
				fmt.Fprintln(out, tr("Steward is up to date.", "Steward 已是最新版本。"))
				return nil
			}
			if check {
				if command != nil {
					fmt.Fprintf(out, tr("Run `steward update` to run: %s\n", "运行 `steward update` 将执行：%s\n"), strings.Join(command, " "))
				} else {
					fmt.Fprintln(out, tr("Run `steward update` to install it.", "运行 `steward update` 安装新版本。"))
				}
				return nil
			}

			if command != nil {
				fmt.Fprintf(out, tr("Running: %s\n", "正在执行：%s\n"), strings.Join(command, " "))
				process := exec.CommandContext(cmd.Context(), command[0], command[1:]...)
				process.Stdin, process.Stdout, process.Stderr = os.Stdin, out, cmd.ErrOrStderr()
				if err := process.Run(); err != nil {
					return fmt.Errorf(tr("%s failed: %w", "%s 执行失败：%w"), command[0], err)
				}
			} else {
				staged, err := releases.Download(ctx, latest, runtime.GOOS, runtime.GOARCH, executable)
				if err != nil {
					if errors.Is(err, os.ErrPermission) {
						return fmt.Errorf(tr("%w; rerun with permission to write %s", "%w；请使用可写入 %s 的权限重新运行"), err, filepath.Dir(executable))
					}
					return err
				}
				if err := update.Replace(staged, executable, runtime.GOOS); err != nil {
					os.Remove(staged)
					return fmt.Errorf(tr("replace %s: %w", "替换 %s 失败：%w"), executable, err)
				}
				fmt.Fprintf(out, tr("Updated Steward to %s at %s\n", "已将 Steward 更新到 %s：%s\n"), latest, executable)
			}
			if running, err := serverRunning(); err == nil && running {
				fmt.Fprintln(out, tr("Restart the running server to use the new version: steward server stop, then steward server start.", "请重启正在运行的服务以使用新版本：先运行 steward server stop，再运行 steward server start。"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, tr("only check for a newer version", "仅检查是否有新版本"))
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
