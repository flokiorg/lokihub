package main

import (
	"fmt"

	"github.com/flokiorg/lokihub/lspd/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/urfave/cli"
)

func getPool(ctx *cli.Context) (*pgxpool.Pool, error) {
	url := ctx.String("database-url")
	if url == "" {
		return nil, fmt.Errorf("database-url is required")
	}

	pool, err := postgres.PgConnect(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return pool, nil
}

func getStore(ctx *cli.Context) (*postgres.RevenueCliStore, error) {
	pool, err := getPool(ctx)
	if err != nil {
		return nil, err
	}

	return postgres.NewCliStore(pool), nil
}
