package version

var BuildCommit = "dev"

func Commit() string { return BuildCommit }
func Short() string {
	if len(BuildCommit) > 7 && BuildCommit != "dev" {
		return BuildCommit[:7]
	}
	return BuildCommit
}
