package cmd

import (
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/easypodcasts/go-worker/internal/worker"
	"github.com/easypodcasts/go-worker/pkg/config"
	"github.com/easypodcasts/go-worker/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newStartCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:           "start",
		Aliases:       []string{"s"},
		Short:         "Start the worker",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			logrus.SetOutput(os.Stdout)

			viper.SetConfigName("config")
			viper.SetEnvPrefix("yml")

			if file == "" {
				viper.AddConfigPath("/etc/easypodcasts/")
				viper.AddConfigPath("$HOME/.easypodcasts/")
				viper.AddConfigPath(".")
			} else {
				viper.AddConfigPath(path.Dir(file))
			}

			err = viper.ReadInConfig()
			if err != nil {
				log.L.Fatalf(err.Error())
				os.Exit(1)
			}

			var c config.Config
			if err := viper.Unmarshal(&c); err != nil {
				log.L.Fatalf("unable to decode config, %v", err)
			}

			w, err := worker.NewWorker(c)
			err = w.Run()
			if err != nil {
				return err
			}

			waitExitSignal(w)
			return nil
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Initialize configuration file")

	return cmd
}

// Wait until program interrupted. When interrupted gracefully shutdown worker.
func waitExitSignal(w *worker.Worker) {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)

	signal.Notify(sigs, syscall.SIGQUIT, syscall.SIGTERM, os.Interrupt)

	go func() {
		sig := <-sigs

		//ctx, cancel := context.WithTimeout(context.Background(), worker.ShutdownTimeout*time.Second)
		//defer cancel()

		err := w.Shutdown(sig)
		if err != nil {
			log.L.WithError(err).Errorln()
			return
		}

		done <- true
	}()
	<-done
}
