package cmd

import (
	"os"

	"github.com/easypodcasts/go-worker/pkg/config"
	"github.com/easypodcasts/go-worker/pkg/log"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:           "config",
		Aliases:       []string{"c"},
		Short:         "Generates a config.yaml file",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			conf, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}

			log.L.Infof(color.New(color.Bold).Sprintf("Generating %s file", file))
			if _, err := conf.WriteString(config.ExampleConfig); err != nil {
				return err
			}

			err = conf.Close()
			if err != nil {
				return err
			}

			log.L.WithField("file", file).Info("config created; please edit accordingly to your needs")
			return nil
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "config.yaml", "Initialize configuration file")

	return cmd
}
