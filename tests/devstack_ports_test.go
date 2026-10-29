package tests

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// loopbackBound reports a compose port mapping published on DEV_BIND, the
// loopback unless the developer chose otherwise.
func loopbackBound(mapping string) bool {
	return strings.HasPrefix(mapping, "${DEV_BIND:-127.0.0.1}:")
}

func TestLoopbackBound_OnlyTheDevBindFormPasses(t *testing.T) {
	assert.True(t, loopbackBound("${DEV_BIND:-127.0.0.1}:${RELAY_PORT:-5500}:5500"))
	for _, m := range []string{"5500:5500", "${RELAY_PORT:-5500}:5500", "0.0.0.0:5500:5500", "${DEV_BIND}:5500:5500", "${DEV_BIND:-0.0.0.0}:5500:5500"} {
		assert.False(t, loopbackBound(m), m)
	}
}

// The dev stack publishes every port on DEV_BIND: its node RPC, relay and Hub
// would otherwise be reachable from the whole network.
func TestDevStack_EveryPublishedPortBindsDevBind(t *testing.T) {
	raw, err := os.ReadFile("../docker-compose.dev.yml")
	require.NoError(t, err)
	var compose struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &compose))
	published := 0
	for name, svc := range compose.Services {
		for _, p := range svc.Ports {
			published++
			assert.True(t, loopbackBound(p), "%s publishes %q on every interface", name, p)
		}
	}
	assert.Positive(t, published)
}
