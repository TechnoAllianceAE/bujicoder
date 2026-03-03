package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/TechnoAllianceAE/bujicoder/cli/app"
	"github.com/TechnoAllianceAE/bujicoder/shared/buildinfo"
	"github.com/TechnoAllianceAE/bujicoder/shared/selfupdate"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Printf("buji %s\n", buildinfo.String())
			return
		case "--help", "-h":
			printUsage()
			return
		case "update":
			if err := selfupdate.ApplyUpdate(context.Background()); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	opts := []tea.ProgramOption{}
	// Enable alt screen for proper scrolling and viewport management.
	opts = append(opts, tea.WithAltScreen())
	// Mouse capture blocks standard terminal drag selection in many terminals.
	// Disabled by default to allow text selection. Users can opt-in via env var.
	if isTrueEnv("BUJICODER_ENABLE_MOUSE") {
		opts = append(opts, tea.WithMouseCellMotion())
	}

	p := tea.NewProgram(
		app.NewModel(buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime),
		opts...,
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func isTrueEnv(name string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func printUsage() {
	fmt.Println(`BujiCoder - AI Coding Assistant

Usage:
  buji              Start interactive TUI
  buji update       Update to the latest version
  buji --version    Show version
  buji --help       Show this help

TUI Commands:
  /mode <mode>       Switch cost mode (normal · heavy · max · plan)
  /new               Start a new conversation
  /history           Browse and resume conversations
  /models            List available models and mode mappings

Environment:
  BUJICODER_CONFIG_DIR            Config directory (default: ~/.bujicoder)
  BUJICODER_ENABLE_MOUSE          Set to 1 to enable mouse capture in TUI
  BUJICODER_DISABLE_UPDATE_CHECK  Skip update check

Both scrolling AND text selection work by default:
- Use terminal scroll (Shift+PageUp/Down, Ctrl+Shift+Up/Down) or mouse wheel to scroll
- Use mouse to select text as usual in your terminal
- Use Ctrl+C to quit

For team features, visit https://bujicoder.com`)
}
