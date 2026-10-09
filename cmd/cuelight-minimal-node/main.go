package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// cuelight-minimal-node deliberately delegates protocol, storage, and signed
// release handling to cuelight-player while selecting the MPV-only profile. It
// never starts Chromium, Node, or Electron and therefore runs on appliance
// images containing only MPV and the player binary.
func main() {
	player, err := exec.LookPath("cuelight-player")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cuelight-minimal-node: cuelight-player is required: %v\n", err)
		os.Exit(1)
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		const prefix = "CUELIGHT_PLAYER_RENDERER_MODE="
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "CUELIGHT_PLAYER_RENDERER_MODE=mpv-only")
	if err := syscall.Exec(player, []string{player}, env); err != nil {
		fmt.Fprintf(os.Stderr, "cuelight-minimal-node: replace process: %v\n", err)
		os.Exit(1)
	}
}
