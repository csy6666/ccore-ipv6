package version

// These values are replaced by release builds through -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
