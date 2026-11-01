package main

import (
	"os"

	"github.com/rs/zerolog/log"

	"github.com/flokiorg/lokihub/lspd/platform/build"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "lspd"
	app.Version = build.GetTag() + " commit=" + build.GetRevision()
	app.Usage = "LSP implementation for LND"
	app.Action = runLspd
	app.Commands = []cli.Command{
		genKeyCommand,
		migrateCommand,
		configCommand,
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal().Msgf("Failed to run app: %v", err)
	}
}
