package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"time"

	"github.com/loomx-ai/steward/internal/checkpoint"
	"github.com/loomx-ai/steward/internal/datadir"
	"github.com/spf13/cobra"
)

// releaseCheckInterval is how often a running server asks again. A one-shot
// check at start-up would miss a bulletin published while it runs.
const releaseCheckInterval = 24 * time.Hour

// releaseCheck carries the answer from the checkpoint service. It is started
// once per process by StartReleaseCheck and read by whichever command reports
// it, so no command ever waits on the network unless it has something to say.
var releaseCheck <-chan *checkpoint.Response

func releaseCheckParams(version string) checkpoint.Params {
	directory, err := datadir.Path()
	if err != nil {
		directory = ""
	}
	return checkpoint.Params{Version: version, Directory: directory}
}

// StartReleaseCheck asks in the background whether a newer release exists and
// whether a security bulletin applies to this build. It returns immediately;
// `steward version` reports the answer, and ReportSecurityAlerts reports a
// bulletin after any command.
func StartReleaseCheck(ctx context.Context, version string) {
	releaseCheck = checkpoint.Start(ctx, releaseCheckParams(version))
}

// alertGrace bounds how long a finished command waits for an advisory. The
// cached answer that almost every run uses arrives at once; the daily run that
// has to ask the service usually beats this, and when it does not the next
// command reports the advisory instead.
const alertGrace = 250 * time.Millisecond

// ReportSecurityAlerts writes any advisory that applies to this build.
func ReportSecurityAlerts(w io.Writer) {
	if releaseCheck == nil {
		return
	}
	timer := time.NewTimer(alertGrace)
	defer timer.Stop()
	select {
	case response := <-releaseCheck:
		writeAlerts(w, response)
	case <-timer.C:
	}
}

func writeAlerts(w io.Writer, response *checkpoint.Response) {
	if response == nil {
		return
	}
	for _, alert := range response.Alerts {
		label := tr("Notice", "提示")
		switch alert.Level {
		case "warn", "warning":
			label = tr("Warning", "警告")
		case "critical", "error":
			label = tr("Security advisory", "安全公告")
		}
		fmt.Fprintf(w, "\n%s: %s\n", label, alert.Message)
		if alert.URL != "" {
			fmt.Fprintf(w, "%s\n", alert.URL)
		}
	}
}

// watchReleases keeps a running server informed. It reports the answer the
// process already asked for, then asks again once a day until ctx ends.
func watchReleases(ctx context.Context, version string) {
	params := releaseCheckParams(version)
	go func() {
		if releaseCheck != nil {
			select {
			case response := <-releaseCheck:
				logRelease(version, response)
			case <-ctx.Done():
				return
			}
		}
		ticker := time.NewTicker(releaseCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if response, err := checkpoint.Check(ctx, params); err == nil {
					logRelease(version, response)
				}
			}
		}
	}()
}

func logRelease(version string, response *checkpoint.Response) {
	if response == nil {
		return
	}
	if response.Outdated && response.CurrentVersion != "" {
		slog.Info("a newer Steward release is available", "running", version, "latest", response.CurrentVersion)
	}
	for _, alert := range response.Alerts {
		slog.Warn("Steward advisory", "level", alert.Level, "message", alert.Message, "url", alert.URL)
	}
}

func newVersionCommand(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: tr("Show the Steward version", "显示 Steward 版本"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Steward v%s\non %s_%s\n", version, runtime.GOOS, runtime.GOARCH)
			if releaseCheck == nil {
				return nil
			}
			response := <-releaseCheck
			if response == nil {
				return nil
			}
			if response.Outdated && response.CurrentVersion != "" {
				fmt.Fprintf(out, tr("\nYour version of Steward is out of date. The latest version is %s.\nRun `steward update` to install it.\n",
					"\n当前 Steward 版本已过期，最新版本为 %s。\n运行 `steward update` 即可安装。\n"), response.CurrentVersion)
			}
			writeAlerts(out, response)
			return nil
		},
	}
}
