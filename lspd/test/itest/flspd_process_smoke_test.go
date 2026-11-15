package itest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFlspdProcessSmoke(t *testing.T) {
	d := StartDevnet(t)

	pg, err := NewPostgresContainer(t.TempDir() + "/postgres.log")
	require.NoError(t, err)
	require.NoError(t, pg.Start(t.Context()))
	// t.Context() is already cancelled when cleanups run.
	t.Cleanup(func() {
		if err := pg.Stop(context.Background()); err != nil {
			t.Logf("stopping postgres: %v", err)
		}
	})

	p := NewFlspdProcess(t, d, pg, FlspdProcessOptions{JitEnabled: true})
	require.NoError(t, p.Start())
	t.Cleanup(func() {
		if err := p.Stop(); err != nil {
			t.Logf("stopping flspd: %v", err)
		}
	})

	conn, err := net.DialTimeout("tcp", p.ListenAddress(), time.Second)
	require.NoError(t, err)
	_ = conn.Close()

	require.NoError(t, p.Restart())

	conn, err = net.DialTimeout("tcp", p.ListenAddress(), time.Second)
	require.NoError(t, err)
	_ = conn.Close()
}
