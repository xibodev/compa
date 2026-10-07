package cron

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v3/pkg/cron"
)

func newAddCommand(open openStore) *cobra.Command {
	var (
		name    string
		message string
		every   int64
		cronExp string
		tz      string
		channel string
		to      string
	)

	cmd := &cobra.Command{
		Use:   "add --name <name> --message <text> (--every <seconds> | --cron <expr>)",
		Short: "Add a new scheduled job",
		Example: `compa-kernel cron add --name standup --message "Remind me about standup" --cron "0 9 * * 1-5"
compa-kernel cron add --name water --message "Drink water" --every 3600`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if every <= 0 && cronExp == "" {
				return fmt.Errorf("either --every or --cron must be specified")
			}
			if tz != "" && cronExp == "" {
				return fmt.Errorf("--tz applies only to --cron schedules")
			}

			var schedule cron.CronSchedule
			if every > 0 {
				everyMS := every * 1000
				schedule = cron.CronSchedule{Kind: "every", EveryMS: &everyMS}
			} else {
				schedule = cron.CronSchedule{Kind: "cron", Expr: cronExp, TZ: tz}
			}
			if err := schedule.Validate(); err != nil {
				return fmt.Errorf("invalid schedule: %w", err)
			}

			cs, err := open()
			if err != nil {
				return err
			}
			job, err := cs.AddJob(name, schedule, message, channel, to)
			if err != nil {
				return fmt.Errorf("error adding job: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ Added job '%s' (%s)\n", job.Name, job.ID)

			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Job name")
	cmd.Flags().StringVarP(&message, "message", "m", "", "Message for agent")
	cmd.Flags().Int64VarP(&every, "every", "e", 0, "Run every N seconds (at least 60)")
	cmd.Flags().StringVarP(&cronExp, "cron", "c", "", "Cron expression (e.g. '0 9 * * *')")
	cmd.Flags().StringVar(&tz, "tz", "", "Time zone for --cron (e.g. 'Europe/Prague'); default: local time")
	cmd.Flags().StringVar(&to, "to", "", "Recipient for delivery")
	cmd.Flags().StringVar(&channel, "channel", "", "Channel for delivery")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("message")
	cmd.MarkFlagsMutuallyExclusive("every", "cron")

	return cmd
}
