package config

import (
	"strconv"
	"testing"

	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/logger"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestConfig(t *testing.T) *config {
	t.Helper()
	logger.Init(strconv.Itoa(int(4)))
	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.Migrate(gdb))
	cfg, err := NewConfig(&AppConfig{Workdir: ".test"}, gdb)
	require.NoError(t, err)
	return cfg
}

func TestImageProxyMode_DefaultOff(t *testing.T) {
	cfg := newTestConfig(t)
	require.Equal(t, ImageProxyModeOff, cfg.GetImageProxyMode())
}

func TestSetImageProxyMode(t *testing.T) {
	cfg := newTestConfig(t)

	require.NoError(t, cfg.SetImageProxyMode(ImageProxyModeDirect))
	require.Equal(t, ImageProxyModeDirect, cfg.GetImageProxyMode())

	require.NoError(t, cfg.SetImageProxyMode(ImageProxyModeProxy))
	require.Equal(t, ImageProxyModeProxy, cfg.GetImageProxyMode())

	// Empty resets to the default.
	require.NoError(t, cfg.SetImageProxyMode(""))
	require.Equal(t, ImageProxyModeOff, cfg.GetImageProxyMode())

	// Unknown modes are rejected and leave the stored value unchanged.
	require.Error(t, cfg.SetImageProxyMode("bogus"))
	require.Equal(t, ImageProxyModeOff, cfg.GetImageProxyMode())
}

func TestValidateImageProxyTemplate(t *testing.T) {
	valid := []string{
		"https://wsrv.nl/?url={url}",
		"http://example.com/{url}",
		"https://imgproxy.example/insecure/plain/{b64url}",
		"/api/circle/avatar-proxy?url={url}",
		"/api/img/{b64url}",
	}
	for _, tpl := range valid {
		require.NoErrorf(t, ValidateImageProxyTemplate(tpl), "expected %q to be valid", tpl)
	}

	invalid := []string{
		"",                                    // no placeholder
		"https://wsrv.nl/?x=1",                // no placeholder
		"https://wsrv.nl/?a={url}&b={url}",    // two placeholders
		"https://wsrv.nl/?a={url}&b={b64url}", // mixed placeholders
		"ftp://host/{url}",                    // wrong scheme
		"wsrv.nl/{url}",                       // not http(s) and not /api/
		"{url}",                               // bare, no host/path
	}
	for _, tpl := range invalid {
		require.Errorf(t, ValidateImageProxyTemplate(tpl), "expected %q to be invalid", tpl)
	}
}

func TestSetImageProxyTemplate(t *testing.T) {
	cfg := newTestConfig(t)

	require.Empty(t, cfg.GetImageProxyTemplate())

	require.NoError(t, cfg.SetImageProxyTemplate("https://wsrv.nl/?url={url}"))
	require.Equal(t, "https://wsrv.nl/?url={url}", cfg.GetImageProxyTemplate())

	// An invalid template is rejected and leaves the stored value unchanged.
	require.Error(t, cfg.SetImageProxyTemplate("https://wsrv.nl/?x=1"))
	require.Equal(t, "https://wsrv.nl/?url={url}", cfg.GetImageProxyTemplate())

	// Empty clears it.
	require.NoError(t, cfg.SetImageProxyTemplate(""))
	require.Empty(t, cfg.GetImageProxyTemplate())
}
