package appstore

type App struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	ExtendedDescription string `json:"extendedDescription"`
	WebLink             string `json:"webLink"`
	PlayLink            string `json:"playLink,omitempty"`
	AppleLink           string `json:"appleLink,omitempty"`
	ZapStoreLink        string `json:"zapStoreLink,omitempty"`
	Category            string `json:"category"`
	Logo                string `json:"logo"`
	InstallGuide        string `json:"installGuide"`
	FinalizeGuide       string `json:"finalizeGuide"`
	Version             string `json:"version"`
	CreatedAt           int64  `json:"createdAt"` // Unix timestamp in seconds
	UpdatedAt           int64  `json:"updatedAt"` // Unix timestamp in seconds
}

type Service interface {
	Start()
	// Sync refreshes the local catalogue from the configured store URL. It is a
	// no-op (no outbound request) when no store URL is configured. Exposed so
	// enabling the store at runtime (Settings → Services) can trigger an
	// immediate refresh instead of waiting for the next periodic tick.
	Sync()
	ListApps() []App
	GetLogoPath(appId string) (string, error)
}
