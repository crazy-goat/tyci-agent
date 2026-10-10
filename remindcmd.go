package main

import (
	"fmt"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/cron"
	"github.com/spf13/cobra"
)

var remindCmd = &cobra.Command{
	Use:   "remind",
	Short: "List or cancel one-shot reminders",
}

var remindListCmd = &cobra.Command{
	Use:   "list",
	Short: "List reminders that are not delivered yet",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		text, err := reminderListText()
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), text)
		return nil
	},
}

var remindCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel a reminder by its id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := cronConfigDir()
		if err != nil {
			return err
		}
		f, err := cron.LoadReminders(dir)
		if err != nil {
			return err
		}
		if !f.Remove(args[0]) {
			return fmt.Errorf("no reminder with id %q", args[0])
		}
		if err := cron.SaveReminders(dir, f); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "cancelled %s\n", args[0])
		return nil
	},
}

func init() {
	remindCmd.AddCommand(remindListCmd, remindCancelCmd)
	rootCmd.AddCommand(remindCmd)
}

// reminderListText returns the undelivered reminders, one line each, or
// "no reminders". The CLI "remind list" and the "/reminders" chat command
// both print it.
func reminderListText() (string, error) {
	dir, err := cronConfigDir()
	if err != nil {
		return "", err
	}
	f, err := cron.LoadReminders(dir)
	if err != nil {
		return "", err
	}
	lines := formatReminders(f.Reminders)
	if len(lines) == 0 {
		return "no reminders", nil
	}
	return strings.Join(lines, "\n"), nil
}

// formatReminders returns one line per undelivered reminder, in file order.
// The line holds the id, the local fire time, the text and the directory.
func formatReminders(rs []cron.Reminder) []string {
	var lines []string
	for _, r := range rs {
		if r.Delivered {
			continue
		}
		text := strings.Join(strings.Fields(r.Text), " ")
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s",
			r.ID, r.FiresAt.Local().Format("2006-01-02 15:04"), text, r.Dir))
	}
	return lines
}
