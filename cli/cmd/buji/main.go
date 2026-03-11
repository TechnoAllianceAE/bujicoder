package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		case "uninstall":
			runUninstall("buji")
			return
		}
	}

	// Check for --verbose flag anywhere in args.
	verbose := false
	for _, arg := range os.Args[1:] {
		if arg == "--verbose" {
			verbose = true
			break
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
		app.NewModel(buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime, verbose),
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
  buji --verbose    Start with verbose logging to stderr
  buji update       Update to the latest version
  buji uninstall    Remove buji and optionally ~/.bujicoder
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
  BUJICODER_LOG_LEVEL             Log level: trace, debug, info, warn, error (default: info)
  BUJICODER_LOG_DIR               Override log directory (default: ~/.bujicoder/logs)

Both scrolling AND text selection work by default:
- Use terminal scroll (Shift+PageUp/Down, Ctrl+Shift+Up/Down) or mouse wheel to scroll
- Use mouse to select text as usual in your terminal
- Use Ctrl+C to quit

For team features, visit https://bujicoder.com`)
}

func runUninstall(binaryName string) {
	fmt.Println("\n  BujiCoder — Uninstaller\n")

	binaryPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: could not determine binary path: %v\n", err)
		os.Exit(1)
	}
	binaryPath, _ = filepath.EvalSymlinks(binaryPath)

	// Confirm with user
	fmt.Printf("  This will remove:\n")
	fmt.Printf("    • %s\n", binaryPath)
	fmt.Printf("\n  Continue? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("  Cancelled.")
		return
	}

	// Remove binary — may need sudo on Unix
	if err := os.Remove(binaryPath); err != nil {
		if os.IsPermission(err) && runtime.GOOS != "windows" {
			fmt.Println("  Permission denied — retrying with sudo...")
			cmd := exec.Command("sudo", "rm", "-f", binaryPath)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "  Error removing binary: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("  ✓ Removed %s\n", binaryPath)

	// Ask about config directory
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".bujicoder")
	if _, err := os.Stat(configDir); err == nil {
		fmt.Printf("\n  Config directory found: %s\n", configDir)
		fmt.Printf("  Contains: API keys, conversations, logs, permissions.\n")
		fmt.Printf("  Remove it? [y/N] ")

		answer, _ = reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "y" || answer == "yes" {
			if err := os.RemoveAll(configDir); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: could not remove %s: %v\n", configDir, err)
			} else {
				fmt.Printf("  ✓ Removed %s\n", configDir)
			}
		} else {
			fmt.Printf("  Kept %s\n", configDir)
		}
	}

	fmt.Println("\n  ✓ BujiCoder has been uninstalled.\n")
}
