package main

import (
	"os"

	"github.com/easypodcasts/go-worker/cmd"
	"github.com/easypodcasts/go-worker/pkg/log"
	"github.com/sirupsen/logrus"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			// log panics forces exit
			if _, ok := r.(*logrus.Entry); ok {
				os.Exit(1)
			}
			panic(r)
		}
	}()

	logrus.SetFormatter(&logrus.TextFormatter{
		DisableColors:   os.Getenv("CI") != "",
		TimestampFormat: log.RFC3339NanoFixed,
	})

	if err := cmd.RootCmdRun(os.Args[1:]); err != nil {
		log.L.Fatalln("Cannot start worker: ", err)
	}
}
