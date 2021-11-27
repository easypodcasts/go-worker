package cmd

import (
	"io"
	"os"

	"github.com/easypodcasts/go-worker/internal/build"
	"github.com/easypodcasts/go-worker/pkg/log"
	"github.com/fatih/color"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func RootCmdRun(args []string) error {
	var verbose string

	var rootCmd = &cobra.Command{
		Use:     "go-worker",
		Short:   "Worker runner for EasyPodcast",
		Version: build.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// enable colored output on CI servers
			if os.Getenv("CI") != "" {
				color.NoColor = false
			}
			if err := setUpLogs(os.Stdout, verbose); err != nil {
				return err
			}
			return nil
		},
	}

	//Default value is the warn level
	rootCmd.PersistentFlags().StringVarP(&verbose, "verbosity", "v", logrus.InfoLevel.String(), "Log level (debug, info, warn, error, fatal, panic")
	rootCmd.SetArgs(args)

	rootCmd.AddCommand(
		newConfigCmd(),
		newStartCmd(),
	)

	err := rootCmd.Execute()
	if err != nil {
		log.L.WithError(err).Errorln()
		return err
	}
	return nil
}

//setUpLogs set the log output ans the log level
func setUpLogs(out io.Writer, level string) error {
	logrus.SetOutput(out)
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return err
	}
	logrus.SetLevel(lvl)
	return nil
}
