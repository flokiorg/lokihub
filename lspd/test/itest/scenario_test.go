package itest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These test the scenario-file parsing/validation logic in isolation --
// no devnet or docker required, unlike TestScenarios in runner_test.go.

func writeScenarioFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestLoadScenario_ValidFile(t *testing.T) {
	path := writeScenarioFile(t, `
name: example
description: a minimal valid scenario
steps:
  - mine_blocks:
      count: 6
assertions:
  - channel_exists:
      between: [lsp, client]
`)
	s, err := loadScenario(path)
	require.NoError(t, err)
	assert.Equal(t, "example", s.Name)
	require.Len(t, s.Steps, 1)
	assert.Equal(t, "mine_blocks", s.Steps[0].Verb)
	require.Len(t, s.Assertions, 1)
	assert.Equal(t, "channel_exists", s.Assertions[0].Verb)
}

func TestLoadScenario_UnknownTopLevelKeyIsHardError(t *testing.T) {
	path := writeScenarioFile(t, `
name: example
oops_typo: true
steps:
  - mine_blocks:
      count: 6
`)
	_, err := loadScenario(path)
	require.Error(t, err)
}

func TestLoadScenario_UnknownStepVerbIsHardError(t *testing.T) {
	path := writeScenarioFile(t, `
name: example
steps:
  - not_a_real_verb:
      count: 6
`)
	_, err := loadScenario(path)
	require.ErrorContains(t, err, "unknown step verb")
}

func TestLoadScenario_UnknownAssertionVerbIsHardError(t *testing.T) {
	path := writeScenarioFile(t, `
name: example
steps:
  - mine_blocks:
      count: 6
assertions:
  - not_a_real_assertion:
      foo: bar
`)
	_, err := loadScenario(path)
	require.ErrorContains(t, err, "unknown assertion verb")
}

func TestLoadScenario_StepMustBeSingleKeyMapping(t *testing.T) {
	path := writeScenarioFile(t, `
name: example
steps:
  - mine_blocks:
      count: 6
    open_channel:
      from: a
      to: b
`)
	_, err := loadScenario(path)
	require.ErrorContains(t, err, "single-key mapping")
}

func TestDecodeParamsStrict_UnknownFieldIsHardError(t *testing.T) {
	path := writeScenarioFile(t, `
name: example
steps:
  - mine_blocks:
      count: 6
      typo_field: oops
`)
	s, err := loadScenario(path)
	require.NoError(t, err) // unknown verb-param fields aren't caught at load time...

	var p mineBlocksParams
	err = decodeParamsStrict(s.Steps[0].Params, &p) // ...only when the verb actually decodes its params.
	require.Error(t, err)
}
