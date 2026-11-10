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
	containerID, dbURLs, err := startTestPostgres()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start test postgres: %v\n", err)
		return 1
	}
	defer func() {
		_ = exec.Command("docker", "rm", "-f", containerID).Run()
	}()

	databaseUrl, err := waitForPostgres(dbURLs, 30*time.Second)
	if err != nil {
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

// startTestPostgres runs a disposable postgres container and returns its
// container ID together with the candidate connection URLs to try, in
// preference order:
//
//   - localhost on the Docker-assigned random host port (random so parallel
//     runs don't collide on a fixed port), which is reachable when the test
//     process shares the daemon's network namespace — running directly on the
//     host, in CI, or on Docker Desktop;
//   - the container's own bridge IP on 5432, which is reachable when the test
//     runs inside a sibling container talking to the daemon over a mounted
//     socket (docker-out-of-docker), where the host-published port is not on
//     the test process's loopback.
//
// waitForPostgres picks whichever address becomes reachable first, so the same
// test passes in both environments.
func startTestPostgres() (containerID string, dbURLs []string, err error) {
	out, err := exec.Command("docker", "run", "-d",
		"-e", "POSTGRES_PASSWORD=postgres",
		"-p", "127.0.0.1::5432",
		"postgres:15-alpine",
	).Output()
	if err != nil {
		return "", nil, fmt.Errorf("docker run: %w", err)
	}
	containerID = strings.TrimSpace(string(out))

	removeContainer := func() { _ = exec.Command("docker", "rm", "-f", containerID).Run() }

	var urls []string
	if portOut, portErr := exec.Command("docker", "port", containerID, "5432/tcp").Output(); portErr == nil {
		// portOut looks like "127.0.0.1:54321"; take the last colon-field.
		parts := strings.Split(strings.TrimSpace(string(portOut)), ":")
		if hostPort := parts[len(parts)-1]; hostPort != "" {
			urls = append(urls, postgresDSN("localhost", hostPort))
		}
	}

	ipOut, err := exec.Command("docker", "inspect", "-f",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", containerID).Output()
	if err != nil {
		removeContainer()
		return "", nil, fmt.Errorf("docker inspect: %w", err)
	}
	if ip := strings.TrimSpace(string(ipOut)); ip != "" {
		urls = append(urls, postgresDSN(ip, "5432"))
	}

	if len(urls) == 0 {
		removeContainer()
		return "", nil, fmt.Errorf("no reachable address for test postgres container %s", containerID)
	}
	return containerID, urls, nil
}

func postgresDSN(host, port string) string {
	return fmt.Sprintf("postgres://postgres:postgres@%s:%s/postgres?sslmode=disable", host, port)
}

// waitForPostgres polls the candidate URLs until one accepts a connection and
// answers a ping, then returns that URL. Trying each candidate every round is
// how the harness discovers which address is actually routable from here
// without caring which environment it runs in.
func waitForPostgres(dbURLs []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		for _, databaseUrl := range dbURLs {
			pool, err := pgxpool.New(context.Background(), databaseUrl)
			if err != nil {
				lastErr = err
				continue
			}
			pingErr := pool.Ping(context.Background())
			pool.Close()
			if pingErr == nil {
				return databaseUrl, nil
			}
			lastErr = pingErr
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for postgres: %w", lastErr)
}
