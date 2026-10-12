package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/selfdrivingjazz/cuelight/internal/buildinfo"
)

// cuelight-minimal-node deliberately delegates protocol, storage, and signed
// release handling to cuelight-player while selecting the MPV-only profile. It
// never starts Chromium, Node, or Electron and therefore runs on appliance
// images containing only MPV and the player binary.
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String("cuelight-minimal-node"))
		return
	}
	player, err := playerPath()
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

// playerPath prefers the cuelight-player shipped beside this binary, so an
// appliance bundle under /opt/cuelight/versions/<v> always runs its own player.
func playerPath() (string, error) {
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			sibling := filepath.Join(filepath.Dir(resolved), "cuelight-player")
			if info, err := os.Stat(sibling); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
				return sibling, nil
			}
		}
	}
	return exec.LookPath("cuelight-player")
}
