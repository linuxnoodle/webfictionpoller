package providers

import "github.com/linuxnoodle/webfictionpoller/internal/plugin"

// init self-registers every compiled-in text provider into plugin.Default.
// Importing the providers package (directly or via blank import) is sufficient
// to make these available; cmd/main.go imports this package for the side effect.
//
// Each provider's Meta() is the source of truth for name/kind/rate/auth modes.
func init() {
	plugin.Default.Register(NewRoyalRoadProvider())
	plugin.Default.Register(NewSpaceBattlesProvider())
	plugin.Default.Register(NewSufficientVelocityProvider())
	plugin.Default.Register(NewQuestionableQuestingProvider())
	plugin.Default.Register(NewFanfictionNetProvider())
	plugin.Default.Register(NewAO3Provider())
}
