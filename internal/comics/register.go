package comics

import "github.com/linuxnoodle/webfictionpoller/internal/plugin"

// init self-registers every compiled-in comic provider into plugin.Default.
func init() {
	plugin.Default.Register(NewMangaDexProvider())
}
