package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/selfdrivingjazz/cuelight/internal/buildinfo"
	operator "github.com/selfdrivingjazz/cuelight/internal/client"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	exitOK       = 0
	exitUsage    = 2
	exitConfig   = 3
	exitAuth     = 4
	exitNotFound = 5
	exitConflict = 6
	exitAPI      = 7
	exitIO       = 8
	exitTimeout  = 9
	// exitHelp is internal: a subcommand printed its help. run maps it to exitOK.
	exitHelp = -1
)

type outputMode string

const (
	outputHuman outputMode = "human"
	outputJSON  outputMode = "json"
	outputJSONL outputMode = "jsonl"
)

type globalOptions struct {
	json                  bool // retained for source compatibility with older callers
	output                outputMode
	configPath            string
	server                string
	token                 string
	caPath                string
	allowInsecureLoopback bool
}

func (o globalOptions) machine() bool { return o.output != outputHuman }

type commandIO struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	httpClient *http.Client
}

func run(ctx context.Context, args []string, streams commandIO) int {
	defaultPath, err := operator.DefaultConfigPath()
	if err != nil {
		fmt.Fprintln(streams.stderr, err)
		return exitConfig
	}
	options := globalOptions{output: outputHuman}
	var outputErr error
	args, options.output, options.json, outputErr = extractOutputArgs(args)
	if outputErr != nil {
		return renderFailure(streams, true, exitUsage, outputErr)
	}
	flags := flag.NewFlagSet("cuelight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.json, "json", options.json, "write machine-readable JSON (deprecated; use --output json)")
	flags.StringVar(&options.configPath, "config", defaultPath, "operator config path")
	flags.StringVar(&options.server, "server", "", "override the configured server")
	flags.StringVar(&options.token, "token", "", "override the configured operator token")
	flags.StringVar(&options.caPath, "ca", "", "CA certificate for the configured HTTPS server")
	flags.BoolVar(&options.allowInsecureLoopback, "allow-insecure-loopback", false, "allow plain HTTP to a loopback server")
	showVersion := flags.Bool("version", false, "print version")
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		writeUsage(streams.stdout)
		return exitOK
	}
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(streams.stderr, err)
		writeUsage(streams.stderr)
		return exitUsage
	}
	if options.output != outputHuman {
		options.json = true
	}
	if *showVersion {
		fmt.Fprintln(streams.stdout, buildinfo.String("cuelight"))
		return exitOK
	}
	remaining := flags.Args()
	if len(remaining) > 1 && isHelpFlag(remaining[1]) {
		writeCommandUsage(streams.stdout, remaining[0])
		return exitOK
	}
	if len(remaining) == 0 {
		writeUsage(streams.stderr)
		return exitUsage
	}

	if code := dispatch(ctx, remaining, options, streams); code != exitHelp {
		return code
	}
	return exitOK
}

func dispatch(ctx context.Context, remaining []string, options globalOptions, streams commandIO) int {
	switch remaining[0] {
	case "login":
		return runLogin(remaining[1:], options, streams)
	case "config":
		return runConfig(remaining[1:], options, streams)
	case "token":
		return runToken(ctx, remaining[1:], options, streams)
	case "enrollment-token", "enrollment":
		return runEnrollment(ctx, remaining[1:], options, streams)
	case "recovery-token", "recovery":
		return runRecovery(ctx, remaining[1:], options, streams)
	case "node":
		return runNode(ctx, remaining[1:], options, streams)
	case "media":
		return runMedia(ctx, remaining[1:], options, streams)
	case "playlist":
		return runPlaylist(ctx, remaining[1:], options, streams)
	case "release":
		return runRelease(ctx, remaining[1:], options, streams)
	case "assignment":
		return runAssignment(ctx, remaining[1:], options, streams)
	case "status":
		return runStatus(ctx, remaining[1:], options, streams)
	case "events":
		return runEvents(ctx, remaining[1:], options, streams)
	case "play":
		return runPlay(ctx, remaining[1:], options, streams)
	case "wait":
		return runWait(ctx, remaining[1:], options, streams)
	case "fixture":
		return runFixture(remaining[1:], options, streams)
	default:
		fmt.Fprintf(streams.stderr, "unknown command %q\n", remaining[0])
		writeUsage(streams.stderr)
		return exitUsage
	}
}

func extractOutputArgs(args []string) ([]string, outputMode, bool, error) {
	mode := outputHuman
	legacy := false
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--json" {
			legacy, mode = true, outputJSON
			continue
		}
		if arg == "--output" {
			if i+1 >= len(args) {
				return nil, mode, legacy, errors.New("--output requires human, json, or jsonl")
			}
			i++
			mode = outputMode(args[i])
			if mode != outputHuman && mode != outputJSON && mode != outputJSONL {
				return nil, mode, legacy, fmt.Errorf("invalid output format %q (want human, json, or jsonl)", args[i])
			}
			continue
		}
		if strings.HasPrefix(arg, "--output=") {
			mode = outputMode(strings.TrimPrefix(arg, "--output="))
			if mode != outputHuman && mode != outputJSON && mode != outputJSONL {
				return nil, mode, legacy, fmt.Errorf("invalid output format %q (want human, json, or jsonl)", mode)
			}
			continue
		}
		remaining = append(remaining, arg)
	}
	return remaining, mode, legacy, nil
}

func runLogin(args []string, options globalOptions, streams commandIO) int {
	flags := commandFlags("login")
	server := flags.String("server", options.server, "Cuelight server URL")
	token := flags.String("token", options.token, "operator token (omit on a trusted network)")
	caPath := flags.String("ca", options.caPath, "CA certificate for the Cuelight server")
	allowInsecureLoopback := flags.Bool("allow-insecure-loopback", options.allowInsecureLoopback, "allow plain HTTP to a loopback server")
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	if flags.NArg() != 0 || *server == "" {
		fmt.Fprintln(streams.stderr, "usage: cuelight login --server URL [--token TOKEN] [--ca PATH]")
		return exitUsage
	}
	options.allowInsecureLoopback = *allowInsecureLoopback
	options.caPath = *caPath
	httpClient, err := configuredHTTPClient(options, streams)
	if err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	if _, err := operator.New(*server, *token, httpClient, clientOptions(options)...); err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	if err := operator.SaveConfig(options.configPath, operator.Config{Server: *server, Token: *token, CAPath: *caPath}); err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	result := struct {
		Server          string `json:"server"`
		TokenConfigured bool   `json:"tokenConfigured"`
	}{Server: strings.TrimRight(*server, "/"), TokenConfigured: *token != ""}
	if options.json {
		writeJSON(streams.stdout, result)
	} else {
		fmt.Fprintf(streams.stdout, "Saved operator configuration for %s\n", result.Server)
	}
	return exitOK
}

func runConfig(args []string, options globalOptions, streams commandIO) int {
	if len(args) > 0 && args[0] == "show" {
		args = args[1:]
	}
	flags := commandFlags("config show")
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(streams.stderr, "usage: cuelight config [show]")
		return exitUsage
	}
	config, err := operator.LoadConfig(options.configPath)
	if err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	result := struct {
		Server          string `json:"server"`
		TokenConfigured bool   `json:"tokenConfigured"`
		CAPath          string `json:"caPath,omitempty"`
	}{Server: config.Server, TokenConfigured: config.Token != "", CAPath: config.CAPath}
	if options.json {
		writeJSON(streams.stdout, result)
	} else {
		tokenState := "configured"
		if !result.TokenConfigured {
			tokenState = "none (requests are unauthenticated; works on control's trusted networks)"
		}
		fmt.Fprintf(streams.stdout, "Server: %s\nToken: %s\n", result.Server, tokenState)
		if result.CAPath != "" {
			fmt.Fprintf(streams.stdout, "CA: %s\n", result.CAPath)
		}
	}
	return exitOK
}

func runToken(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 {
		fmt.Fprintln(streams.stderr, "usage: cuelight token <list|revoke>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		if code := parseCommand(commandFlags("token list"), args[1:], streams); code != exitOK {
			return code
		}
		if len(args) != 1 {
			return usageError(streams.stderr, "usage: cuelight token list")
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		items, err := api.ListTokens(ctx)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.output == outputJSONL {
			for _, item := range items {
				writeJSONL(streams.stdout, item)
			}
		} else if options.json {
			writeJSON(streams.stdout, struct {
				Items []operator.AdminToken `json:"items"`
			}{Items: items})
		} else if len(items) == 0 {
			fmt.Fprintln(streams.stdout, "No operator tokens.")
		} else {
			fmt.Fprintln(streams.stdout, "ID\tPREFIX\tSCOPES\tSTATE")
			for _, item := range items {
				state := "active"
				if item.Revoked {
					state = "revoked"
				}
				fmt.Fprintf(streams.stdout, "%s\t%s\t%s\t%s\n", item.ID, item.Prefix, strings.Join(item.Scopes, ","), state)
			}
		}
		return exitOK
	case "revoke":
		flags := commandFlags("token revoke")
		key := flags.String("idempotency-key", "", "idempotency key")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cuelight token revoke [--idempotency-key KEY] TOKEN_ID")
		}
		resolvedKey, err := mutationKey(*key)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, err)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		id := flags.Arg(0)
		if err := api.RevokeToken(ctx, id, resolvedKey); err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
			writeJSON(streams.stdout, struct {
				ID      string `json:"id"`
				Revoked bool   `json:"revoked"`
			}{ID: id, Revoked: true})
		} else {
			fmt.Fprintf(streams.stdout, "Revoked operator token %s\n", id)
		}
		return exitOK
	default:
		return usageError(streams.stderr, "usage: cuelight token <list|revoke>")
	}
}

func runEnrollment(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || args[0] != "create" {
		return usageError(streams.stderr, "usage: cuelight enrollment-token create [--idempotency-key KEY]")
	}
	flags := commandFlags("enrollment-token create")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams); code != exitOK {
		return code
	}
	if flags.NArg() != 0 {
		return usageError(streams.stderr, "usage: cuelight enrollment-token create [--idempotency-key KEY]")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	resolvedKey, err := mutationKey(*key)
	if err != nil {
		return renderFailure(streams, options.json, exitIO, err)
	}
	created, err := api.CreateEnrollmentToken(ctx, resolvedKey)
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	printSecret(streams.stdout, options.output, created)
	return exitOK
}

func runRecovery(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || args[0] != "create" {
		return usageError(streams.stderr, "usage: cuelight recovery-token create [--idempotency-key KEY] NODE_ID")
	}
	flags := commandFlags("recovery-token create")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams); code != exitOK {
		return code
	}
	if flags.NArg() != 1 {
		return usageError(streams.stderr, "usage: cuelight recovery-token create [--idempotency-key KEY] NODE_ID")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	resolvedKey, err := mutationKey(*key)
	if err != nil {
		return renderFailure(streams, options.json, exitIO, err)
	}
	created, err := api.CreateRecoveryToken(ctx, flags.Arg(0), resolvedKey)
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	printSecret(streams.stdout, options.output, created)
	return exitOK
}

func runNode(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 {
		return usageError(streams.stderr, "usage: cuelight node <list|show|remote-link>")
	}
	flags := commandFlags("node " + args[0])
	if code := parseCommand(flags, args[1:], streams); code != exitOK {
		return code
	}
	args = append([]string{args[0]}, flags.Args()...)
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usageError(streams.stderr, "usage: cuelight node list")
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		items, err := api.ListNodes(ctx)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.output == outputJSONL {
			for _, item := range items {
				writeJSONL(streams.stdout, item)
			}
		} else if options.json {
			writeJSON(streams.stdout, struct {
				Items []operator.Node `json:"items"`
			}{Items: items})
		} else if len(items) == 0 {
			fmt.Fprintln(streams.stdout, "No nodes.")
		} else {
			fmt.Fprintln(streams.stdout, "ID\tNAME\tCREDENTIAL\tDESIRED\tOBSERVED")
			for _, item := range items {
				fmt.Fprintf(streams.stdout, "%s\t%s\t%s\t%d\t%d\n", item.ID, item.Name, item.CredentialState, item.DesiredGeneration, item.ObservedGeneration)
			}
		}
		return exitOK
	case "show":
		if len(args) != 2 {
			return usageError(streams.stderr, "usage: cuelight node show NODE_ID")
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		node, err := api.GetNode(ctx, args[1])
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
			writeJSON(streams.stdout, node)
		} else {
			fmt.Fprintf(streams.stdout, "ID: %s\nName: %s\nCredential: %s\nDesired generation: %d\nObserved generation: %d\n", node.ID, node.Name, node.CredentialState, node.DesiredGeneration, node.ObservedGeneration)
		}
		return exitOK
	case "remote-link":
		if len(args) != 2 {
			return usageError(streams.stderr, "usage: cuelight node remote-link NODE_ID")
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		link, err := api.CreateNodeRemoteLink(ctx, args[1])
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
			writeJSON(streams.stdout, link)
		} else {
			fmt.Fprintln(streams.stdout, link.RemoteURL)
		}
		return exitOK
	default:
		return usageError(streams.stderr, "usage: cuelight node <list|show|remote-link>")
	}
}

func runMedia(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 {
		return usageError(streams.stderr, "usage: cuelight media <list|upload|register-local|add-web>")
	}
	switch args[0] {
	case "list":
		const usage = "usage: cuelight media list [--kind file|web] [--limit N]"
		flags := commandFlags("media list")
		kind := flags.String("kind", "", "only file or web media")
		limit := flags.Int("limit", 0, "maximum items to list (default: all)")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 0 || (*kind != "" && *kind != "file" && *kind != "web") || *limit < 0 {
			return usageError(streams.stderr, usage)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		items, err := collectPages(ctx, *limit, func(ctx context.Context, request operator.PageRequest) (operator.Page[operator.Document], error) {
			request.Filters = map[string]string{"kind": *kind}
			return api.ListMediaPage(ctx, request)
		})
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		return printDocuments(streams, options.output, items, "No media.", "ID\tKIND\tTITLE\tURL/TYPE", func(item operator.Document) string {
			detail := documentString(item, "url")
			if artifact, ok := item["artifact"].(map[string]any); ok && detail == "" {
				detail, _ = artifact["mediaType"].(string)
			}
			return fmt.Sprintf("%s\t%s\t%s\t%s", documentString(item, "id"), documentString(item, "kind"), documentString(item, "title"), detail)
		})
	case "upload":
		flags := commandFlags("media upload")
		name := flags.String("name", "", "asset name (defaults to file name)")
		mediaType := flags.String("media-type", "", "media type (defaults from extension)")
		key := flags.String("idempotency-key", "", "idempotency key")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cuelight media upload [flags] FILE")
		}
		path := flags.Arg(0)
		file, err := os.Open(path)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, fmt.Errorf("open upload: %w", err))
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return renderFailure(streams, options.json, exitIO, fmt.Errorf("stat upload: %w", err))
		}
		if !info.Mode().IsRegular() || info.Size() < 1 {
			return renderFailure(streams, options.json, exitIO, errors.New("upload must be a non-empty regular file"))
		}
		resolvedName := *name
		if resolvedName == "" {
			resolvedName = filepath.Base(path)
		}
		resolvedType := *mediaType
		if resolvedType == "" {
			resolvedType = mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
			if resolvedType == "" {
				resolvedType = "application/octet-stream"
			}
		}
		resolvedKey, err := mutationKey(*key)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, err)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		asset, err := api.UploadAsset(ctx, resolvedName, resolvedType, info.Size(), file, resolvedKey)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		printAsset(streams.stdout, options.output, asset)
		return exitOK
	case "register-local":
		flags := commandFlags("media register-local")
		key := flags.String("idempotency-key", "", "idempotency key")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cuelight media register-local [--idempotency-key KEY] PATH")
		}
		resolvedKey, err := mutationKey(*key)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, err)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		asset, err := api.RegisterLocalAsset(ctx, flags.Arg(0), resolvedKey)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		printAsset(streams.stdout, options.output, asset)
		return exitOK
	case "add-web":
		const usage = "usage: cuelight media add-web --url URL [--title TITLE] [--app-access]"
		flags := commandFlags("media add-web")
		webURL := flags.String("url", "", "absolute http(s) URL of the page")
		title := flags.String("title", "", "library title (defaults to the URL)")
		appAccess := flags.Bool("app-access", false, "present the page as a Cuelight app with an app token")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 0 || *webURL == "" {
			return usageError(streams.stderr, usage)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		input := map[string]any{"kind": "web", "url": *webURL, "title": *title}
		if *appAccess {
			input["appAccess"] = true
		}
		item, err := api.CreateMedia(ctx, input, "")
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
			writeJSON(streams.stdout, item)
		} else {
			printDocument(streams.stdout, item)
		}
		return exitOK
	default:
		return usageError(streams.stderr, "usage: cuelight media <list|upload|register-local|add-web>")
	}
}

func runPlaylist(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	const groupUsage = "usage: cuelight playlist <list|show|create>"
	if len(args) == 0 {
		return usageError(streams.stderr, groupUsage)
	}
	switch args[0] {
	case "list":
		const usage = "usage: cuelight playlist list [--limit N]"
		flags := commandFlags("playlist list")
		limit := flags.Int("limit", 0, "maximum playlists to list (default: all)")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 0 || *limit < 0 {
			return usageError(streams.stderr, usage)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		items, err := collectPages(ctx, *limit, api.ListPlaylistsPage)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		return printDocuments(streams, options.output, items, "No playlists.", "ID\tNAME\tITEMS\tVERSION", func(item operator.Document) string {
			entries, _ := item["items"].([]any)
			return fmt.Sprintf("%s\t%s\t%d\t%d", documentString(item, "id"), documentString(item, "name"), len(entries), documentInt64(item, "version"))
		})
	case "show":
		flags := commandFlags("playlist show")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cuelight playlist show PLAYLIST_ID")
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		playlist, err := api.GetPlaylist(ctx, flags.Arg(0))
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
			writeJSON(streams.stdout, playlist)
			return exitOK
		}
		printPlaylist(streams.stdout, playlist)
		return exitOK
	case "create":
		const usage = "usage: cuelight playlist create --name NAME --item MEDIA_ID:SECONDS [--item ...] [--description TEXT] [--idempotency-key KEY]"
		flags := commandFlags("playlist create")
		name := flags.String("name", "", "playlist name")
		description := flags.String("description", "", "playlist description")
		var items playlistItemsFlag
		flags.Var(&items, "item", "MEDIA_ID:SECONDS, repeatable, in play order; every item needs a positive duration")
		key := flags.String("idempotency-key", "", "idempotency key")
		if code := parseCommand(flags, args[1:], streams); code != exitOK {
			return code
		}
		if flags.NArg() != 0 || *name == "" || len(items) == 0 {
			return usageError(streams.stderr, usage)
		}
		resolvedKey, err := mutationKey(*key)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, err)
		}
		api, code := configuredClient(options, streams)
		if code != exitOK {
			return code
		}
		playlist, err := api.CreatePlaylist(ctx, operator.PlaylistInput{Name: *name, Description: *description, Items: items}, resolvedKey)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
			writeJSON(streams.stdout, playlist)
			return exitOK
		}
		printPlaylist(streams.stdout, playlist)
		return exitOK
	default:
		return usageError(streams.stderr, groupUsage)
	}
}

// playlistItemsFlag collects repeated --item MEDIA_ID[:SECONDS] values in
// order. A missing duration is sent as 0 so the server's validation message
// names the offending item.
type playlistItemsFlag []operator.PlaylistItemInput

func (f *playlistItemsFlag) String() string { return "" }

func (f *playlistItemsFlag) Set(value string) error {
	mediaID, seconds := value, 0
	if index := strings.LastIndex(value, ":"); index >= 0 {
		mediaID = value[:index]
		if _, err := fmt.Sscan(value[index+1:], &seconds); err != nil || seconds < 1 || fmt.Sprint(seconds) != value[index+1:] {
			return fmt.Errorf("item %q: SECONDS must be a positive integer", value)
		}
	}
	if mediaID == "" {
		return fmt.Errorf("item %q: missing MEDIA_ID", value)
	}
	*f = append(*f, operator.PlaylistItemInput{MediaID: mediaID, Position: len(*f) + 1, DurationSeconds: seconds})
	return nil
}

// collectPages follows server cursors until exhausted or limit items (0 = all).
func collectPages(ctx context.Context, limit int, fetch func(context.Context, operator.PageRequest) (operator.Page[operator.Document], error)) ([]operator.Document, error) {
	items := []operator.Document{}
	request := operator.PageRequest{Limit: 100}
	for {
		if limit > 0 {
			request.Limit = min(100, limit-len(items))
		}
		page, err := fetch(ctx, request)
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if !page.Page.HasMore || page.Page.NextCursor == nil || len(page.Items) == 0 || (limit > 0 && len(items) >= limit) {
			return items, nil
		}
		request.Cursor = *page.Page.NextCursor
	}
}

func printDocuments(streams commandIO, mode outputMode, items []operator.Document, empty, header string, row func(operator.Document) string) int {
	switch {
	case mode == outputJSONL:
		for _, item := range items {
			writeJSONL(streams.stdout, item)
		}
	case mode == outputJSON:
		writeJSON(streams.stdout, struct {
			Items []operator.Document `json:"items"`
		}{Items: items})
	case len(items) == 0:
		fmt.Fprintln(streams.stdout, empty)
	default:
		fmt.Fprintln(streams.stdout, header)
		for _, item := range items {
			fmt.Fprintln(streams.stdout, row(item))
		}
	}
	return exitOK
}

func printPlaylist(output io.Writer, playlist operator.Document) {
	fmt.Fprintf(output, "ID: %s\nName: %s\nVersion: %d\n", documentString(playlist, "id"), documentString(playlist, "name"), documentInt64(playlist, "version"))
	if description := documentString(playlist, "description"); description != "" {
		fmt.Fprintf(output, "Description: %s\n", description)
	}
	entries, _ := playlist["items"].([]any)
	fmt.Fprintln(output, "POSITION\tMEDIA\tSECONDS\tTITLE")
	for _, entry := range entries {
		item, _ := entry.(map[string]any)
		source := documentString(item, "mediaId")
		if source == "" {
			source = documentString(item, "assetId")
		}
		fmt.Fprintf(output, "%d\t%s\t%d\t%s\n", documentInt64(item, "position"), source, documentInt64(item, "durationSeconds"), documentString(item, "title"))
	}
}

func documentString(document map[string]any, key string) string {
	value, _ := document[key].(string)
	return value
}

func runRelease(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || args[0] != "publish" {
		return usageError(streams.stderr, "usage: cuelight release publish [--file FILE] [--idempotency-key KEY]")
	}
	flags := commandFlags("release publish")
	filePath := flags.String("file", "-", "publish input JSON file, or - for stdin")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams); code != exitOK {
		return code
	}
	if flags.NArg() != 0 {
		return usageError(streams.stderr, "usage: cuelight release publish [--file FILE] [--idempotency-key KEY]")
	}
	var source io.Reader = streams.stdin
	var file *os.File
	if *filePath != "-" {
		opened, err := os.Open(*filePath)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, fmt.Errorf("open release input: %w", err))
		}
		file = opened
		defer file.Close()
		source = file
	}
	input, err := io.ReadAll(source)
	if err != nil {
		return renderFailure(streams, options.json, exitIO, fmt.Errorf("read release input: %w", err))
	}
	if !json.Valid(input) {
		return renderFailure(streams, options.json, exitUsage, errors.New("release input is not valid JSON"))
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	resolvedKey, err := mutationKey(*key)
	if err != nil {
		return renderFailure(streams, options.json, exitIO, err)
	}
	release, err := api.PublishRelease(ctx, input, resolvedKey)
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	if options.json {
		writeJSON(streams.stdout, release)
	} else {
		printDocument(streams.stdout, release)
	}
	return exitOK
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--h" || arg == "-help" || arg == "--help"
}

func runAssignment(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || (args[0] != "set" && args[0] != "assign") {
		return usageError(streams.stderr, "usage: cuelight assignment set [flags] NODE_ID")
	}
	flags := commandFlags("assignment set")
	mediaID := flags.String("media-id", "", "media item to show")
	playlistID := flags.String("playlist-id", "", "playlist to loop")
	mode := flags.String("delivery-mode", "", "stream or cache_then_play (defaults from the release)")
	remote := addRemoteFlags(flags)
	generation := flags.Int64("expected-generation", -1, "expected assignment generation")
	ifMatch := flags.String("if-match", "", "current assignment ETag")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams); code != exitOK {
		return code
	}
	if flags.NArg() != 1 || (*mediaID == "") == (*playlistID == "") || *generation < 0 || *ifMatch == "" || (*mode != "" && *mode != "stream" && *mode != "cache_then_play") {
		return usageError(streams.stderr, "usage: cuelight assignment set (--media-id ID|--playlist-id ID) --expected-generation N --if-match ETAG [flags] NODE_ID")
	}
	remoteInput, showQR, err := remote.values()
	if err != nil {
		return usageError(streams.stderr, err.Error())
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	resolvedKey, err := mutationKey(*key)
	if err != nil {
		return renderFailure(streams, options.json, exitIO, err)
	}
	assignment, err := api.PutAssignment(ctx, flags.Arg(0), *ifMatch, resolvedKey, operator.AssignmentInput{
		MediaID:            *mediaID,
		PlaylistID:         *playlistID,
		DeliveryMode:       *mode,
		ExpectedGeneration: *generation,
		RemoteInput:        remoteInput,
		ShowQR:             showQR,
	})
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	if options.json {
		writeJSON(streams.stdout, assignment)
	} else {
		fmt.Fprintf(streams.stdout, "Assigned release %s to node %s at desired generation %d\n", assignment.ReleaseID, flags.Arg(0), assignment.DesiredGeneration)
	}
	return exitOK
}

func runStatus(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	flags := commandFlags("status")
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	args = flags.Args()
	if len(args) != 1 {
		return usageError(streams.stderr, "usage: cuelight status NODE_ID")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	status, err := api.GetStatus(ctx, args[0])
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	if options.json {
		writeJSON(streams.stdout, status)
	} else {
		printDocument(streams.stdout, status)
	}
	return exitOK
}

func runEvents(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	flags := commandFlags("events")
	cursor := flags.String("cursor", "", "pagination cursor")
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	if flags.NArg() != 1 {
		return usageError(streams.stderr, "usage: cuelight events [--cursor CURSOR] NODE_ID")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	page, err := api.ListEvents(ctx, flags.Arg(0), *cursor)
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	if options.output == outputJSONL {
		for _, event := range page.Items {
			writeJSONL(streams.stdout, event)
		}
	} else if options.json {
		writeJSON(streams.stdout, page)
	} else if len(page.Items) == 0 {
		fmt.Fprintln(streams.stdout, "No events.")
	} else {
		for _, event := range page.Items {
			fmt.Fprintf(streams.stdout, "%v\t%v\t%v\n", event["occurredAt"], event["type"], event["id"])
		}
		if page.NextCursor != nil {
			fmt.Fprintf(streams.stdout, "Next cursor: %s\n", *page.NextCursor)
		}
	}
	return exitOK
}

func runPlay(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	const usage = "usage: cuelight play [--file FILE|--asset ASSET_ID|--media MEDIA_ID|--url URL] [--remote-input|--show-qr|--no-remote] NODE_ID"
	flags := commandFlags("play")
	filePath := flags.String("file", "", "media file to upload")
	assetID := flags.String("asset", "", "existing asset ID")
	mediaID := flags.String("media", "", "existing media ID")
	webURL := flags.String("url", "", "web page URL to add to the library and show")
	name := flags.String("name", "", "asset name")
	mediaType := flags.String("media-type", "", "media type")
	mode := flags.String("delivery-mode", "", "stream or cache_then_play (defaults from the release)")
	timeout := flags.Duration("timeout", 30*time.Second, "wait timeout")
	interval := flags.Duration("interval", 250*time.Millisecond, "poll interval")
	noWait := flags.Bool("no-wait", false, "do not wait for convergence")
	key := flags.String("idempotency-key", "", "idempotency key (generated when omitted)")
	remote := addRemoteFlags(flags)
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	sources := 0
	for _, source := range []string{*filePath, *assetID, *mediaID, *webURL} {
		if source != "" {
			sources++
		}
	}
	if flags.NArg() != 1 || sources != 1 || *timeout < 0 || *interval <= 0 {
		return usageError(streams.stderr, usage)
	}
	if *mode != "" && *mode != "stream" && *mode != "cache_then_play" {
		return usageError(streams.stderr, "play --delivery-mode must be stream or cache_then_play")
	}
	remoteInput, showQR, err := remote.values()
	if err != nil {
		return usageError(streams.stderr, err.Error())
	}
	resolvedKey, err := mutationKey(*key)
	if err != nil {
		return renderFailure(streams, options.json, exitIO, err)
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	nodeID := flags.Arg(0)
	target := *mediaID
	switch {
	case *webURL != "":
		item, err := api.CreateMedia(ctx, map[string]any{"kind": "web", "url": *webURL}, "")
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		target, _ = item["id"].(string)
	case *filePath != "" || *assetID != "":
		asset := operator.Asset{ID: *assetID, Name: *assetID}
		if *filePath != "" {
			file, err := os.Open(*filePath)
			if err != nil {
				return renderFailure(streams, options.json, exitIO, fmt.Errorf("open play media: %w", err))
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				if err == nil {
					err = errors.New("play media must be a non-empty regular file")
				}
				return renderFailure(streams, options.json, exitIO, err)
			}
			resolvedName := *name
			if resolvedName == "" {
				resolvedName = filepath.Base(*filePath)
			}
			resolvedType := *mediaType
			if resolvedType == "" {
				resolvedType = mime.TypeByExtension(strings.ToLower(filepath.Ext(*filePath)))
				if resolvedType == "" {
					resolvedType = "application/octet-stream"
				}
			}
			asset, err = api.UploadAsset(ctx, resolvedName, resolvedType, info.Size(), file, resolvedKey)
			if err != nil {
				return renderAPIError(streams, options.json, err)
			}
		}
		item, found, err := api.MediaForAsset(ctx, asset.ID)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if !found {
			item, err = api.CreateMedia(ctx, map[string]any{
				"assetId": asset.ID, "title": asset.Name, "tags": []string{},
				"playback": map[string]any{"fit": "fit", "loop": false, "muted": false, "volume": 100, "orientation": 0},
			}, resolvedKey+"-media")
			if err != nil {
				return renderAPIError(streams, options.json, err)
			}
		}
		target, _ = item["id"].(string)
	}
	if target == "" {
		return renderFailure(streams, options.json, exitAPI, errors.New("media response omitted an id"))
	}
	return convergePlay(ctx, api, options, streams, nodeID, operator.AssignmentInput{MediaID: target, DeliveryMode: *mode, RemoteInput: remoteInput, ShowQR: showQR}, resolvedKey, *timeout, *interval, *noWait)
}

// convergePlay assigns input over the node's current generation and waits
// for the node to converge.
func convergePlay(ctx context.Context, api *operator.Client, options globalOptions, streams commandIO, nodeID string, input operator.AssignmentInput, key string, timeout, interval time.Duration, noWait bool) int {
	current, err := api.GetAssignment(ctx, nodeID)
	if err != nil {
		var problem *operator.Problem
		if !errors.As(err, &problem) || problem.Status != http.StatusNotFound {
			return renderAPIError(streams, options.json, err)
		}
	}
	expected := int64(0)
	if current.DesiredGeneration > 0 {
		expected = current.DesiredGeneration
	}
	input.ExpectedGeneration = expected
	assignment, err := api.PutAssignment(ctx, nodeID, fmt.Sprintf("\"generation:%d\"", expected), key, input)
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	if noWait {
		return renderResult(streams, options.output, assignment)
	}
	return waitForConvergence(ctx, api, options, streams, nodeID, assignment.DesiredGeneration, "", timeout, interval)
}

func runWait(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	flags := commandFlags("wait")
	generation := flags.Int64("generation", -1, "desired generation to observe")
	condition := flags.String("condition", "", "status condition value")
	timeout := flags.Duration("timeout", 30*time.Second, "maximum wait duration")
	interval := flags.Duration("interval", 250*time.Millisecond, "poll interval")
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	if flags.NArg() != 1 || (*generation < 0 && *condition == "") || *timeout < 0 || *interval <= 0 {
		return usageError(streams.stderr, "usage: cuelight wait [--generation N|--condition CONDITION] [--timeout DURATION] NODE_ID")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	return waitForConvergence(ctx, api, options, streams, flags.Arg(0), *generation, *condition, *timeout, *interval)
}
func waitForConvergence(ctx context.Context, api *operator.Client, options globalOptions, streams commandIO, nodeID string, generation int64, condition string, timeout, interval time.Duration) int {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last operator.Document
	for {
		status, err := api.GetStatus(ctx, nodeID)
		if err == nil {
			last = status
			observed := documentInt64(status, "observedGeneration")
			if observed == 0 {
				observed = documentInt64(status, "desiredGenerationSeen")
			}
			ok := generation < 0 || observed >= generation
			if condition != "" {
				ok = ok && documentCondition(status, condition)
			}
			if ok {
				return renderResult(streams, options.output, map[string]any{
					"node": nodeID, "generation": observed, "condition": condition, "status": status,
				})
			}
		} else {
			var problem *operator.Problem
			if !errors.As(err, &problem) || problem.Status != http.StatusNotFound {
				return renderAPIError(streams, options.json, err)
			}
		}
		select {
		case <-ctx.Done():
			return waitFailure(ctx, api, options, streams, nodeID, generation, condition, last)
		case <-deadline.C:
			return waitFailure(ctx, api, options, streams, nodeID, generation, condition, last)
		case <-ticker.C:
		}
	}
}

func documentInt64(document operator.Document, key string) int64 {
	value, ok := document[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		var result int64
		_, _ = fmt.Sscan(fmt.Sprint(value), &result)
		return result
	}
}

func waitFailure(ctx context.Context, api *operator.Client, options globalOptions, streams commandIO, nodeID string, generation int64, condition string, status operator.Document) int {
	events, err := api.ListEvents(ctx, nodeID, "")
	if err != nil {
		events = operator.EventPage{}
	}
	problem := &operator.Problem{Type: "about:blank", Title: "Convergence timeout", Status: http.StatusGatewayTimeout, Detail: fmt.Sprintf("node %s did not reach requested state", nodeID), Extensions: map[string]any{
		"node": nodeID, "generation": generation, "condition": condition, "statusSnapshot": status, "events": events.Items,
	}}
	return renderAPIError(streams, options.json, problem)
}

func documentCondition(status operator.Document, wanted string) bool {
	for _, key := range []string{"condition", "state", "status"} {
		if value, ok := status[key]; ok && fmt.Sprint(value) == wanted {
			return true
		}
	}
	if conditions, ok := status["conditions"].([]any); ok {
		for _, value := range conditions {
			if item, ok := value.(map[string]any); ok && (fmt.Sprint(item["type"]) == wanted || fmt.Sprint(item["status"]) == wanted) {
				return true
			}
		}
	}
	return false
}

func runFixture(args []string, options globalOptions, streams commandIO) int {
	flags := commandFlags("fixture")
	root := flags.String("root", "test/fixtures", "fixture root directory")
	if code := parseCommand(flags, args, streams); code != exitOK {
		return code
	}
	if flags.NArg() < 1 || (flags.Arg(0) != "list" && flags.Arg(0) != "path") {
		return usageError(streams.stderr, "usage: cuelight fixture [--root DIR] <list|path NAME>")
	}
	var files []string
	err := filepath.WalkDir(*root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			rel, _ := filepath.Rel(*root, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return renderFailure(streams, options.json, exitIO, fmt.Errorf("read fixtures: %w", err))
	}
	sort.Strings(files)
	if flags.Arg(0) == "list" {
		if options.output == outputJSONL {
			for _, file := range files {
				writeJSONL(streams.stdout, map[string]string{"name": strings.TrimSuffix(file, filepath.Ext(file)), "path": filepath.Join(*root, file)})
			}
		} else {
			return renderResult(streams, options.output, map[string]any{"root": *root, "fixtures": files})
		}
		return exitOK
	}
	if flags.NArg() != 2 {
		return usageError(streams.stderr, "usage: cuelight fixture [--root DIR] path NAME")
	}
	name := flags.Arg(1)
	for _, file := range files {
		base := strings.TrimSuffix(file, filepath.Ext(file))
		if base == name || file == name || filepath.Base(base) == name {
			return renderResult(streams, options.output, map[string]string{"name": base, "path": filepath.Join(*root, file)})
		}
	}
	return renderFailure(streams, options.json, exitNotFound, fmt.Errorf("fixture %q not found", name))
}

func renderResult(streams commandIO, mode outputMode, value any) int {
	if mode == outputJSONL {
		writeJSONL(streams.stdout, value)
		return exitOK
	}
	if mode == outputJSON {
		writeJSON(streams.stdout, value)
		return exitOK
	}
	if document, ok := value.(operator.Document); ok {
		printDocument(streams.stdout, document)
	} else {
		encoded, _ := json.Marshal(value)
		fmt.Fprintln(streams.stdout, string(encoded))
	}
	return exitOK
}

func configuredClient(options globalOptions, streams commandIO) (*operator.Client, int) {
	server := options.server
	token := options.token
	if server == "" || token == "" {
		config, err := operator.LoadConfig(options.configPath)
		// --server without a saved config is a complete, token-less setup.
		if err != nil && !(server != "" && errors.Is(err, os.ErrNotExist)) {
			return nil, renderFailure(streams, options.json, exitConfig, err)
		}
		if server == "" {
			server = config.Server
		}
		if token == "" {
			token = config.Token
		}
		if options.caPath == "" {
			options.caPath = config.CAPath
		}
	}
	httpClient, err := configuredHTTPClient(options, streams)
	if err != nil {
		return nil, renderFailure(streams, options.json, exitConfig, err)
	}
	api, err := operator.New(server, token, httpClient, clientOptions(options)...)
	if err != nil {
		return nil, renderFailure(streams, options.json, exitConfig, err)
	}
	return api, exitOK
}

func configuredHTTPClient(options globalOptions, streams commandIO) (*http.Client, error) {
	if options.caPath != "" {
		return operator.HTTPClientWithCA(options.caPath)
	}
	return streams.httpClient, nil
}

func clientOptions(options globalOptions) []operator.Option {
	if options.allowInsecureLoopback {
		return []operator.Option{operator.AllowInsecureLoopback()}
	}
	return nil
}

func commandFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// remoteFlags are a command's phone remote flags. Flags left unset keep the
// screen's current remote settings.
type remoteFlags struct {
	flags                         *flag.FlagSet
	remoteInput, showQR, noRemote *bool
}

func addRemoteFlags(flags *flag.FlagSet) remoteFlags {
	return remoteFlags{
		flags:       flags,
		remoteInput: flags.Bool("remote-input", false, "accept phone remote input (default: keep the screen's setting)"),
		showQR:      flags.Bool("show-qr", false, "show the phone remote QR code; implies --remote-input (default: keep the screen's setting)"),
		noRemote:    flags.Bool("no-remote", false, "turn phone remote input and the QR code off"),
	}
}

// values returns the assignment's remote fields; nil keeps the screen's value.
func (r remoteFlags) values() (remoteInput, showQR *bool, err error) {
	set := map[string]bool{}
	r.flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["remote-input"] {
		remoteInput = r.remoteInput
	}
	if set["show-qr"] {
		showQR = r.showQR
	}
	if set["no-remote"] && *r.noRemote {
		if (remoteInput != nil && *remoteInput) || (showQR != nil && *showQR) {
			return nil, nil, errors.New("--no-remote conflicts with --remote-input and --show-qr")
		}
		off := false
		return &off, &off, nil
	}
	if showQR != nil && *showQR {
		if remoteInput != nil && !*remoteInput {
			return nil, nil, errors.New("--show-qr requires remote input")
		}
		on := true
		remoteInput = &on
	}
	return remoteInput, showQR, nil
}

// parseCommand parses subcommand flags. -h/--help prints the flags to stdout
// and returns exitHelp, so callers stop before loading config or calling the API.
func parseCommand(flags *flag.FlagSet, args []string, streams commandIO) int {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(streams.stdout, "usage: cuelight %s [flags]\n", flags.Name())
			flags.SetOutput(streams.stdout)
			flags.PrintDefaults()
			return exitHelp
		}
		fmt.Fprintln(streams.stderr, err)
		return exitUsage
	}
	return exitOK
}

func mutationKey(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func renderAPIError(streams commandIO, jsonOutput bool, err error) int {
	var problem *operator.Problem
	if !errors.As(err, &problem) {
		return renderFailure(streams, jsonOutput, exitIO, err)
	}
	code := exitAPI
	switch problem.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = exitAuth
	case http.StatusNotFound:
		code = exitNotFound
	case http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity:
		code = exitConflict
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		code = exitTimeout
	}
	safe := redactProblem(problem)
	if jsonOutput {
		writeJSON(streams.stderr, safe)
	} else {
		fmt.Fprintf(streams.stderr, "%s", safe.Title)
		if safe.Detail != "" {
			fmt.Fprintf(streams.stderr, ": %s", safe.Detail)
		}
		fmt.Fprintf(streams.stderr, " (HTTP %d)", safe.Status)
		if safe.CorrelationID != "" {
			fmt.Fprintf(streams.stderr, " [correlation %s]", safe.CorrelationID)
		}
		fmt.Fprintln(streams.stderr)
	}
	return code
}

func redactProblem(problem *operator.Problem) *operator.Problem {
	if problem == nil {
		return nil
	}
	safe := *problem
	safe.Extensions = redactMap(problem.Extensions)
	return &safe
}

func redactMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "password") || strings.Contains(lower, "authorization") ||
			strings.Contains(lower, "privatekey") {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = redactValue(value)
	}
	return result
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = redactValue(typed[index])
		}
		return result
	default:
		return value
	}
}

func renderFailure(streams commandIO, jsonOutput bool, code int, err error) int {
	message := redactText(err.Error())
	if jsonOutput {
		writeJSON(streams.stderr, struct {
			Error string `json:"error"`
		}{Error: message})
	} else {
		fmt.Fprintln(streams.stderr, message)
	}
	return code
}

func redactText(value string) string {
	for _, marker := range []string{"secret=", "token=", "password=", "Bearer "} {
		for {
			index := strings.Index(strings.ToLower(value), strings.ToLower(marker))
			if index < 0 {
				break
			}
			start := index + len(marker)
			end := start
			for end < len(value) && !strings.ContainsRune(" \t\r\n,;)]}", rune(value[end])) {
				end++
			}
			value = value[:start] + "[REDACTED]" + value[end:]
		}
	}
	return value
}
func printSecret(output io.Writer, mode outputMode, created operator.OneTimeToken) {
	if mode == outputJSONL {
		writeJSONL(output, created)
	} else if mode == outputJSON {
		writeJSON(output, created)
	} else {
		fmt.Fprintf(output, "ID: %s\nExpires: %s\nSecret: %s\n", created.ID, created.ExpiresAt, created.Secret)
		if created.JoinURL != "" {
			fmt.Fprintf(output, "Join link: %s\n", created.JoinURL)
		}
	}
}

func printAsset(output io.Writer, mode outputMode, asset operator.Asset) {
	if mode == outputJSONL {
		writeJSONL(output, asset)
	} else if mode == outputJSON {
		writeJSON(output, asset)
	} else {
		fmt.Fprintf(output, "Asset: %s (%s)\nArtifact: %s (%d bytes, %s)\n", asset.Name, asset.ID, asset.Artifact.Digest, asset.Artifact.Size, asset.Artifact.MediaType)
	}
}

func printDocument(output io.Writer, document operator.Document) {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, _ := json.Marshal(document[key])
		fmt.Fprintf(output, "%s: %s\n", key, value)
	}
}

func writeJSON(output io.Writer, value any) {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeJSONL(output io.Writer, value any) {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, message)
	return exitUsage
}
func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: cuelight [global flags] <command>")
	fmt.Fprintln(output, "global flags: --output human|json|jsonl, --json, --config PATH, --server URL, --token TOKEN, --ca PATH, --allow-insecure-loopback")
	fmt.Fprintln(output, "commands: login, config, token, enrollment-token, recovery-token, node, media, playlist, release, assignment, status, events, play, wait, fixture")
	fmt.Fprintln(output, "help: cuelight <command> --help lists subcommands; cuelight <command> <subcommand> --help lists its flags")
	fmt.Fprintln(output, "exit codes: 0 success, 2 usage, 3 config, 4 auth, 5 not found, 6 conflict, 7 API, 8 I/O, 9 timeout")
}

// commandUsage lists each command's forms; `cuelight <command> --help` prints them.
var commandUsage = map[string][]string{
	"login":            {"cuelight login --server URL [--token TOKEN] [--ca PATH]"},
	"config":           {"cuelight config [show]"},
	"token":            {"cuelight token list", "cuelight token revoke [--idempotency-key KEY] TOKEN_ID"},
	"enrollment-token": {"cuelight enrollment-token create [--idempotency-key KEY]"},
	"recovery-token":   {"cuelight recovery-token create [--idempotency-key KEY] NODE_ID"},
	"node":             {"cuelight node list", "cuelight node show NODE_ID", "cuelight node remote-link NODE_ID"},
	"media": {
		"cuelight media list [--kind file|web] [--limit N]",
		"cuelight media upload [--name NAME] [--media-type TYPE] [--idempotency-key KEY] FILE",
		"cuelight media register-local [--idempotency-key KEY] PATH",
		"cuelight media add-web --url URL [--title TITLE] [--app-access]",
	},
	"playlist": {
		"cuelight playlist list [--limit N]",
		"cuelight playlist show PLAYLIST_ID",
		"cuelight playlist create --name NAME --item MEDIA_ID:SECONDS [--item ...] [--description TEXT] [--idempotency-key KEY]",
	},
	"release":    {"cuelight release publish [--file FILE] [--idempotency-key KEY]"},
	"assignment": {"cuelight assignment set (--media-id ID|--playlist-id ID) --expected-generation N --if-match ETAG [flags] NODE_ID"},
	"status":     {"cuelight status NODE_ID"},
	"events":     {"cuelight events [--cursor CURSOR] NODE_ID"},
	"play":       {"cuelight play [--file FILE|--asset ASSET_ID|--media MEDIA_ID|--url URL] [--remote-input|--show-qr|--no-remote] [--timeout DURATION] NODE_ID"},
	"wait":       {"cuelight wait [--generation N|--condition CONDITION] [--timeout DURATION] NODE_ID"},
	"fixture":    {"cuelight fixture [--root DIR] <list|path NAME>"},
}

func writeCommandUsage(output io.Writer, command string) {
	switch command {
	case "enrollment":
		command = "enrollment-token"
	case "recovery":
		command = "recovery-token"
	}
	forms, ok := commandUsage[command]
	if !ok {
		fmt.Fprintf(output, "unknown command %q\n", command)
		writeUsage(output)
		return
	}
	fmt.Fprintln(output, "usage:")
	for _, form := range forms {
		fmt.Fprintf(output, "  %s\n", form)
	}
	if len(forms) > 1 {
		fmt.Fprintf(output, "run `cuelight %s <subcommand> --help` for its flags\n", command)
	}
	fmt.Fprintln(output, "output: --output human|json|jsonl (or legacy --json)")
}
