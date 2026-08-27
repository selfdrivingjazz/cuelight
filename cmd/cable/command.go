package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	operator "github.com/jmill/cable/internal/client"
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
)

type globalOptions struct {
	json                  bool
	configPath            string
	server                string
	token                 string
	allowInsecureLoopback bool
}

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
	options := globalOptions{}
	flags := flag.NewFlagSet("cable", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.json, "json", false, "write machine-readable JSON")
	flags.StringVar(&options.configPath, "config", defaultPath, "operator config path")
	flags.StringVar(&options.server, "server", "", "override the configured server")
	flags.StringVar(&options.token, "token", "", "override the configured operator token")
	flags.BoolVar(&options.allowInsecureLoopback, "allow-insecure-loopback", false, "allow plain HTTP to a loopback server")
	showVersion := flags.Bool("version", false, "print version")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(streams.stderr, err)
		writeUsage(streams.stderr)
		return exitUsage
	}
	if *showVersion {
		fmt.Fprintf(streams.stdout, "cable %s\n", version)
		return exitOK
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		writeUsage(streams.stderr)
		return exitUsage
	}

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
	case "release":
		return runRelease(ctx, remaining[1:], options, streams)
	case "assignment":
		return runAssignment(ctx, remaining[1:], options, streams)
	case "status":
		return runStatus(ctx, remaining[1:], options, streams)
	case "events":
		return runEvents(ctx, remaining[1:], options, streams)
	default:
		fmt.Fprintf(streams.stderr, "unknown command %q\n", remaining[0])
		writeUsage(streams.stderr)
		return exitUsage
	}
}

func runLogin(args []string, options globalOptions, streams commandIO) int {
	flags := commandFlags("login")
	server := flags.String("server", options.server, "Cable server URL")
	token := flags.String("token", options.token, "operator token")
	allowInsecureLoopback := flags.Bool("allow-insecure-loopback", options.allowInsecureLoopback, "allow plain HTTP to a loopback server")
	if code := parseCommand(flags, args, streams.stderr); code != exitOK {
		return code
	}
	if flags.NArg() != 0 || *server == "" || *token == "" {
		fmt.Fprintln(streams.stderr, "usage: cable login --server URL --token TOKEN")
		return exitUsage
	}
	options.allowInsecureLoopback = *allowInsecureLoopback
	if _, err := operator.New(*server, *token, streams.httpClient, clientOptions(options)...); err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	if err := operator.SaveConfig(options.configPath, operator.Config{Server: *server, Token: *token}); err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	result := struct {
		Server          string `json:"server"`
		TokenConfigured bool   `json:"tokenConfigured"`
	}{Server: strings.TrimRight(*server, "/"), TokenConfigured: true}
	if options.json {
		writeJSON(streams.stdout, result)
	} else {
		fmt.Fprintf(streams.stdout, "Saved operator configuration for %s\n", result.Server)
	}
	return exitOK
}

func runConfig(args []string, options globalOptions, streams commandIO) int {
	if len(args) == 1 && args[0] == "show" {
		args = nil
	}
	if len(args) != 0 {
		fmt.Fprintln(streams.stderr, "usage: cable config [show]")
		return exitUsage
	}
	config, err := operator.LoadConfig(options.configPath)
	if err != nil {
		return renderFailure(streams, options.json, exitConfig, err)
	}
	result := struct {
		Server          string `json:"server"`
		TokenConfigured bool   `json:"tokenConfigured"`
	}{Server: config.Server, TokenConfigured: config.Token != ""}
	if options.json {
		writeJSON(streams.stdout, result)
	} else {
		fmt.Fprintf(streams.stdout, "Server: %s\nToken: configured\n", result.Server)
	}
	return exitOK
}

func runToken(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 {
		fmt.Fprintln(streams.stderr, "usage: cable token <list|revoke>")
		return exitUsage
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usageError(streams.stderr, "usage: cable token list")
		}
		items, err := api.ListTokens(ctx)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
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
		if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cable token revoke [--idempotency-key KEY] TOKEN_ID")
		}
		resolvedKey, err := mutationKey(*key)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, err)
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
		return usageError(streams.stderr, "usage: cable token <list|revoke>")
	}
}

func runEnrollment(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || args[0] != "create" {
		return usageError(streams.stderr, "usage: cable enrollment-token create [--idempotency-key KEY]")
	}
	flags := commandFlags("enrollment-token create")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
		return code
	}
	if flags.NArg() != 0 {
		return usageError(streams.stderr, "usage: cable enrollment-token create [--idempotency-key KEY]")
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
	printSecret(streams.stdout, options.json, created)
	return exitOK
}

func runRecovery(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || args[0] != "create" {
		return usageError(streams.stderr, "usage: cable recovery-token create [--idempotency-key KEY] NODE_ID")
	}
	flags := commandFlags("recovery-token create")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
		return code
	}
	if flags.NArg() != 1 {
		return usageError(streams.stderr, "usage: cable recovery-token create [--idempotency-key KEY] NODE_ID")
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
	printSecret(streams.stdout, options.json, created)
	return exitOK
}

func runNode(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 {
		return usageError(streams.stderr, "usage: cable node <list|show>")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usageError(streams.stderr, "usage: cable node list")
		}
		items, err := api.ListNodes(ctx)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		if options.json {
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
			return usageError(streams.stderr, "usage: cable node show NODE_ID")
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
	default:
		return usageError(streams.stderr, "usage: cable node <list|show>")
	}
}

func runMedia(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 {
		return usageError(streams.stderr, "usage: cable media <upload|register-local>")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	switch args[0] {
	case "upload":
		flags := commandFlags("media upload")
		name := flags.String("name", "", "asset name (defaults to file name)")
		mediaType := flags.String("media-type", "", "media type (defaults from extension)")
		key := flags.String("idempotency-key", "", "idempotency key")
		if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cable media upload [flags] FILE")
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
		asset, err := api.UploadAsset(ctx, resolvedName, resolvedType, info.Size(), file, resolvedKey)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		printAsset(streams.stdout, options.json, asset)
		return exitOK
	case "register-local":
		flags := commandFlags("media register-local")
		key := flags.String("idempotency-key", "", "idempotency key")
		if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
			return code
		}
		if flags.NArg() != 1 {
			return usageError(streams.stderr, "usage: cable media register-local [--idempotency-key KEY] PATH")
		}
		resolvedKey, err := mutationKey(*key)
		if err != nil {
			return renderFailure(streams, options.json, exitIO, err)
		}
		asset, err := api.RegisterLocalAsset(ctx, flags.Arg(0), resolvedKey)
		if err != nil {
			return renderAPIError(streams, options.json, err)
		}
		printAsset(streams.stdout, options.json, asset)
		return exitOK
	default:
		return usageError(streams.stderr, "usage: cable media <upload|register-local>")
	}
}

func runRelease(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || args[0] != "publish" {
		return usageError(streams.stderr, "usage: cable release publish [--file FILE] [--idempotency-key KEY]")
	}
	flags := commandFlags("release publish")
	filePath := flags.String("file", "-", "publish input JSON file, or - for stdin")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
		return code
	}
	if flags.NArg() != 0 {
		return usageError(streams.stderr, "usage: cable release publish [--file FILE] [--idempotency-key KEY]")
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

func runAssignment(ctx context.Context, args []string, options globalOptions, streams commandIO) int {
	if len(args) == 0 || (args[0] != "set" && args[0] != "assign") {
		return usageError(streams.stderr, "usage: cable assignment set [flags] NODE_ID")
	}
	flags := commandFlags("assignment set")
	releaseID := flags.String("release-id", "", "release ID")
	digest := flags.String("release-digest", "", "release digest")
	mode := flags.String("delivery-mode", "stream", "stream or cache_then_play")
	generation := flags.Int64("expected-generation", -1, "expected assignment generation")
	ifMatch := flags.String("if-match", "", "current assignment ETag")
	key := flags.String("idempotency-key", "", "idempotency key")
	if code := parseCommand(flags, args[1:], streams.stderr); code != exitOK {
		return code
	}
	if flags.NArg() != 1 || *releaseID == "" || *digest == "" || *generation < 0 || *ifMatch == "" || (*mode != "stream" && *mode != "cache_then_play") {
		return usageError(streams.stderr, "usage: cable assignment set --release-id ID --release-digest DIGEST --expected-generation N --if-match ETAG [flags] NODE_ID")
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
		ReleaseID:          *releaseID,
		ReleaseDigest:      *digest,
		DeliveryMode:       *mode,
		ExpectedGeneration: *generation,
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
	if len(args) != 1 {
		return usageError(streams.stderr, "usage: cable status NODE_ID")
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
	if code := parseCommand(flags, args, streams.stderr); code != exitOK {
		return code
	}
	if flags.NArg() != 1 {
		return usageError(streams.stderr, "usage: cable events [--cursor CURSOR] NODE_ID")
	}
	api, code := configuredClient(options, streams)
	if code != exitOK {
		return code
	}
	page, err := api.ListEvents(ctx, flags.Arg(0), *cursor)
	if err != nil {
		return renderAPIError(streams, options.json, err)
	}
	if options.json {
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

func configuredClient(options globalOptions, streams commandIO) (*operator.Client, int) {
	server := options.server
	token := options.token
	if server == "" || token == "" {
		config, err := operator.LoadConfig(options.configPath)
		if err != nil {
			return nil, renderFailure(streams, options.json, exitConfig, err)
		}
		if server == "" {
			server = config.Server
		}
		if token == "" {
			token = config.Token
		}
	}
	api, err := operator.New(server, token, streams.httpClient, clientOptions(options)...)
	if err != nil {
		return nil, renderFailure(streams, options.json, exitConfig, err)
	}
	return api, exitOK
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

func parseCommand(flags *flag.FlagSet, args []string, stderr io.Writer) int {
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
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
	}
	if jsonOutput {
		writeJSON(streams.stderr, problem)
	} else {
		fmt.Fprintf(streams.stderr, "%s", problem.Title)
		if problem.Detail != "" {
			fmt.Fprintf(streams.stderr, ": %s", problem.Detail)
		}
		fmt.Fprintf(streams.stderr, " (HTTP %d)", problem.Status)
		if problem.CorrelationID != "" {
			fmt.Fprintf(streams.stderr, " [correlation %s]", problem.CorrelationID)
		}
		fmt.Fprintln(streams.stderr)
	}
	return code
}

func renderFailure(streams commandIO, jsonOutput bool, code int, err error) int {
	if jsonOutput {
		writeJSON(streams.stderr, struct {
			Error string `json:"error"`
		}{Error: err.Error()})
	} else {
		fmt.Fprintln(streams.stderr, err)
	}
	return code
}

func printSecret(output io.Writer, jsonOutput bool, created operator.OneTimeToken) {
	if jsonOutput {
		writeJSON(output, created)
	} else {
		fmt.Fprintf(output, "ID: %s\nExpires: %s\nSecret: %s\n", created.ID, created.ExpiresAt, created.Secret)
	}
}

func printAsset(output io.Writer, jsonOutput bool, asset operator.Asset) {
	if jsonOutput {
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

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, message)
	return exitUsage
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: cable [--json] [--config PATH] [--allow-insecure-loopback] <command>")
	fmt.Fprintln(output, "commands: login, config, token, enrollment-token, recovery-token, node, media, release, assignment, status, events")
}
