package tui

import (
	"strings"
	"time"
)

// Mutti looks after you, too. Search the palette for uwu.
var muttiSays = []string{
	"Did you eat something today? Containers can't live on caffeine either.",
	"Your containers are all tucked in. Now go to bed.",
	"Put on a jacket, it's cold outside of the Docker network.",
	"I cleaned up your old worktrees. You're welcome. (mutti tidy)",
	"Unhealthy again? That's what you get for skipping the health checks.",
	"Call your mother. Or at least run mutti status.",
	"I'm not angry, I'm just disappointed in your exit code.",
	"Don't force-push on an empty stomach.",
}

func uwu(s styles) string {
	line := muttiSays[time.Now().Second()%len(muttiSays)]
	art := strings.Join([]string{
		`       .--.`,
		`      ( ^^ )  uwu`,
		`    .-'\__/'-.`,
		`   /  mutti   \`,
		`   \ <3 <3 <3 /`,
		`    '-.____.-'`,
	}, "\n")
	return s.accentText.Render(art) + "\n\n" + s.title.Render("Mutti says:") + "\n" + s.muted.Render(line) + "\n\n" + s.faintText.Render("(any key to go back to work)")
}
