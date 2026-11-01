package main

import (
	"os"

	"github.com/rs/zerolog/log"

	"github.com/flokiorg/lokihub/lspd/platform/build"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "flspd_revenue_cli"
	app.Version = build.GetTag() + " commit=" + build.GetRevision()
	app.Usage = "get revenue data from flspd"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:     "database-url",
			Usage:    "postgres database url for flspd",
			Required: true,
		},
	}
	app.Commands = []cli.Command{
		exportForwardsCommand,
		revenueCommand,
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal().Msgf("Failed to run app: %v", err)
	}
}
