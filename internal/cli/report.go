package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/jturan/jade/internal/telemetry"
	"github.com/spf13/cobra"
)

func newReportCmd() *cobra.Command {
	var (
		path string
		unit int
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarize what agent runs cost and whether they worked",
		Long: "The role defaults are educated guesses. This is how you find out\n" +
			"whether a cheap builder that triggers retries actually costs less\n" +
			"than an expensive one that gets it right.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			events, err := telemetry.Read(path)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if len(events) == 0 {
				fmt.Fprintln(out, dimStyle.Render("no runs recorded yet — telemetry is written by jade build"))
				return nil
			}

			if unit > 0 {
				return reportUnit(out, events, unit)
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ROLE\tMODEL\tEFFORT\tRUNS\tOK\tBLOCKED\tERR\tSUCCESS\tMEAN")
			for _, s := range telemetry.Summarize(events) {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%.0f%%\t%s\n",
					s.Role, orDefault(s.Model), orDefault(s.Effort),
					s.Runs, s.OK, s.Blocked, s.Errors,
					s.SuccessRate()*100, duration(s.MeanSeconds()))
			}
			if err := w.Flush(); err != nil {
				return err
			}

			units, retried := unitTotals(events)
			if units > 0 {
				fmt.Fprintf(out, "\n%s\n", dimStyle.Render(fmt.Sprintf(
					"%d unit(s) completed; %d needed at least one retry", units, retried)))
			}
			fmt.Fprintf(out, "%s\n", dimStyle.Render(
				"Success rate and mean duration ignore harness errors — a pane that failed to start says nothing about a model."))
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&path, "file", "", "telemetry log to read (default: ~/.local/share/jade/telemetry.jsonl)")
	f.IntVar(&unit, "unit", 0, "show the full history for one unit instead of a summary")
	return cmd
}

func reportUnit(out interface{ Write([]byte) (int, error) }, events []telemetry.Event, unit int) error {
	rows := telemetry.ForUnit(events, unit)
	if len(rows) == 0 {
		fmt.Fprintf(out, "%s\n", dimStyle.Render(fmt.Sprintf("no runs recorded for #%d", unit)))
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WHEN\tROLE\tMODEL\tATTEMPT\tOUTCOME\tTOOK\tDETAIL")
	for _, e := range rows {
		// A unit row summarizes the whole unit, so it has no model of its own.
		model := orDefault(e.Model)
		if e.Role == telemetry.RoleUnit {
			model = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.At.Format("15:04:05"), e.Role, model,
			attempt(e.Attempt), e.Outcome, duration(e.Seconds), truncate(e.Detail, 48))
	}
	return w.Flush()
}

func attempt(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

func duration(seconds float64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%.0fs", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%.1fm", seconds/60)
	default:
		return fmt.Sprintf("%.1fh", seconds/3600)
	}
}

// unitTotals counts completed units and how many needed a retry — the number
// that decides whether a cheaper builder is really cheaper.
func unitTotals(events []telemetry.Event) (total, retried int) {
	for _, e := range events {
		if e.Role != telemetry.RoleUnit {
			continue
		}
		total++
		if e.Retries > 0 {
			retried++
		}
	}
	return total, retried
}

// telemetryRecorder adapts the telemetry log to the build loop's Recorder.
type telemetryRecorder struct{ log *telemetry.Log }

// Record deliberately swallows errors: losing a telemetry line is never worth
// failing a build that otherwise succeeded.
func (t telemetryRecorder) Record(e telemetry.Event) {
	if t.log != nil {
		_ = t.log.Append(e)
	}
}
