package version

var (
	BuildCommit = "dev"
	BuildTime   = "unknown"
)

func Commit() string { return BuildCommit }
func Time() string   { return BuildTime }
func Short() string {
	if len(BuildCommit) > 7 && BuildCommit != "dev" {
		return BuildCommit[:7]
	}
	return BuildCommit
}
