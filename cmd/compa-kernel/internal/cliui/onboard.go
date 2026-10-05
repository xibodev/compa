package cliui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// PrintOnboardComplete prints the post-onboard “ready” message and next steps.
func PrintOnboardComplete(logo string, configPath string) {
	if !UseFancyLayout() {
		printOnboardPlain(logo, configPath)
		return
	}
	printOnboardFancy(logo, configPath)
}

func printOnboardPlain(logo string, configPath string) {
	fmt.Printf("\n%s Compa is ready!\n", logo)
	fmt.Println("\nNext steps:")
	fmt.Println(indentLines(buildOnboardingSteps(configPath), "  "))
	fmt.Println("")
	fmt.Println(indentLines("Recommended:\n"+recommendedBlock(), "     "))
	fmt.Println("")
	fmt.Println(indentLines(chatStep(), "  "))
}

func indentLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func printOnboardFancy(logo string, configPath string) {
	inner := InnerWidth()
	box := borderStyle().MaxWidth(inner + 8)

	ready := titleBarStyle().Render(logo+" Compa is ready!") + "\n"
	fmt.Println()
	fmt.Println(box.Width(inner).Render(strings.TrimSpace(ready)))
	fmt.Println()

	steps := buildOnboardingSteps(configPath)
	rec := recommendedBlock()
	chat := chatStep()

	if UseColumnLayout() {
		leftW := min(inner/2-2, 52)
		rightW := inner - leftW - 4
		if rightW < 36 {
			rightW = 36
		}
		leftBlock := borderStyle().MaxWidth(leftW + 8).Width(leftW).
			Render(titleBarStyle().Render("Next steps") + "\n\n" + bodyStyle().Width(leftW).Render(steps))
		rightBlock := borderStyle().MaxWidth(rightW + 8).Width(rightW).
			Render(mutedStyle().Bold(true).Render("Recommended") + "\n\n" + bodyStyle().Width(rightW).Render(rec))
		gap := strings.Repeat(" ", 2)
		fmt.Println(lipgloss.JoinHorizontal(lipgloss.Top, leftBlock, gap, rightBlock))
		fmt.Println()
		full := borderStyle().Width(inner).Render(bodyStyle().Width(inner - 4).Render(chat))
		fmt.Println(full)
		return
	}

	// Same order as plain output: numbered steps → recommended → chat line.
	next := titleBarStyle().Render("Next steps") + "\n\n" +
		bodyStyle().Width(inner-4).Render(steps+"\n\n"+rec+"\n\n"+chat)
	fmt.Println(borderStyle().Width(inner).Render(next))
}

// buildOnboardingSteps lists what to do before the first chat. API keys are
// added with `auth login` (or the dashboard), which keeps them in auth.json.
func buildOnboardingSteps(configPath string) string {
	var b strings.Builder
	b.WriteString("1. Connect a model provider:\n")
	b.WriteString("   compa-kernel auth login --provider openai   (or anthropic)\n")
	b.WriteString("   or use the web dashboard.\n")
	b.WriteString("   Settings: ")
	b.WriteString(configPath)
	b.WriteString("\n")
	return b.String()
}

func recommendedBlock() string {
	return "• OpenRouter: https://openrouter.ai/keys\n  (access 100+ models)\n\n" +
		"• Ollama: https://ollama.com\n  (local, free)"
}

func chatStep() string {
	return "2. Chat:\n   compa-kernel agent -m \"Hello!\""
}
