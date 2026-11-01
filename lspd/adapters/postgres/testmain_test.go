package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool is a package-wide connection pool to a disposable, migrated
// Postgres container, set up once in TestMain and shared by every test in
// this package. Tests are responsible for using distinct keys (payment
// hashes, order IDs, etc.) so they don't collide with each other when run
// against the same container.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	if os.Getenv("SKIP_POSTGRES_TESTS") != "" {
		os.Exit(0)
	}
	// os.Exit skips deferred calls, so every exit path returns from run
	// first: the container is removed whatever fails after it started.
	os.Exit(runWithTestPostgres(m))
}

func runWithTestPostgres(m *testing.M) int {
	containerID, hostPort, err := startTestPostgres()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start test postgres: %v\n", err)
		return 1
	}
	defer func() {
		_ = exec.Command("docker", "rm", "-f", containerID).Run()
	}()

	databaseUrl := fmt.Sprintf("postgres://postgres:postgres@localhost:%s/postgres?sslmode=disable", hostPort)

	if err := waitForPostgres(databaseUrl, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "test postgres never became ready: %v\n", err)
		return 1
	}

	if err := Migrate(databaseUrl); err != nil {
		fmt.Fprintf(os.Stderr, "failed to migrate test postgres: %v\n", err)
		return 1
	}

	pool, err := pgxpool.New(context.Background(), databaseUrl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to test postgres: %v\n", err)
		return 1
	}
	defer pool.Close()
	testPool = pool

	return m.Run()
}

// startTestPostgres runs a disposable postgres container on a
// Docker-assigned random host port (so parallel test runs don't collide
// on a fixed port) and returns its container ID and the assigned port.
func startTestPostgres() (containerID string, hostPort string, err error) {
	out, err := exec.Command("docker", "run", "-d",
		"-e", "POSTGRES_PASSWORD=postgres",
		"-p", "127.0.0.1::5432",
		"postgres:15-alpine",
	).Output()
	if err != nil {
		return "", "", fmt.Errorf("docker run: %w", err)
	}
	containerID = strings.TrimSpace(string(out))

	portOut, err := exec.Command("docker", "port", containerID, "5432/tcp").Output()
	if err != nil {
		_ = exec.Command("docker", "rm", "-f", containerID).Run()
		return "", "", fmt.Errorf("docker port: %w", err)
	}
	// portOut looks like "127.0.0.1:54321"
	parts := strings.Split(strings.TrimSpace(string(portOut)), ":")
	hostPort = parts[len(parts)-1]
	return containerID, hostPort, nil
}

func waitForPostgres(databaseUrl string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(context.Background(), databaseUrl)
		if err == nil {
			pingErr := pool.Ping(context.Background())
			pool.Close()
			if pingErr == nil {
				return nil
			}
			lastErr = pingErr
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for postgres: %w", lastErr)
}
