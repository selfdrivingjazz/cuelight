package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/buildinfo"
	"github.com/jmilldotdev/cuelight/internal/control/pki"
)

const (
	envFileName = "cuelight-control.env"
	// Server roots are long-lived; leaves stay within the 825-day limit Apple
	// platforms enforce for TLS server certificates even under private roots.
	serverRootLifetime  = 10 * 365 * 24 * time.Hour
	serverLeafLifetime  = 825 * 24 * time.Hour
	nodeCALifetime      = 10 * 365 * 24 * time.Hour
	certificateBackdate = 5 * time.Minute
)

type stringList []string

func (list *stringList) String() string { return strings.Join(*list, ",") }
func (list *stringList) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*list = append(*list, item)
		}
	}
	return nil
}

type initOptions struct {
	dir         string
	hostnames   []string
	ips         []string
	publicURL   string
	tlsCert     string
	tlsKey      string
	listen      string
	databaseURL string
	networkName string
	existsOK    bool
}

// initLayout is every path `init` creates beneath --dir.
type initLayout struct {
	dir, env, pki, tls, cas, tmp                     string
	nodeCA, nodeCAKey, publisherKey, sealKey         string
	serverCert, serverKey, serverRoot, serverRootKey string
}

func newInitLayout(dir string) initLayout {
	join := func(parts ...string) string { return filepath.Join(append([]string{dir}, parts...)...) }
	return initLayout{
		dir: dir, env: join(envFileName), pki: join("pki"), tls: join("tls"), cas: join("cas"), tmp: join("tmp"),
		nodeCA: join("pki", "node-ca.pem"), nodeCAKey: join("pki", "node-ca-key.pem"),
		publisherKey: join("pki", "publisher-key.pem"), sealKey: join("pki", "seal.key"),
		serverCert: join("tls", "server.pem"), serverKey: join("tls", "server-key.pem"),
		serverRoot: join("tls", "server-root.pem"), serverRootKey: join("tls", "server-root-key.pem"),
	}
}

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options initOptions
	var hostnames, ips stringList
	flags.StringVar(&options.dir, "dir", "", "directory for generated keys, certificates, CAS, and the env file (required)")
	flags.Var(&hostnames, "hostname", "DNS name for the generated TLS certificate (repeatable or comma-separated)")
	flags.Var(&ips, "ip", "IP address for the generated TLS certificate (repeatable or comma-separated)")
	flags.StringVar(&options.publicURL, "public-url", "", "public HTTPS origin nodes and browsers use, e.g. https://cuelight.lan:8443")
	flags.StringVar(&options.tlsCert, "tls-cert", "", "existing TLS certificate chain (e.g. `tailscale cert` output); skips TLS generation")
	flags.StringVar(&options.tlsKey, "tls-key", "", "private key for --tls-cert")
	flags.StringVar(&options.listen, "listen", ":8443", "listen address written to the env file")
	flags.StringVar(&options.databaseURL, "database-url", "", "PostgreSQL URL written to the env file (default: left for you to fill in)")
	flags.StringVar(&options.networkName, "network-name", "", "network name shown in join links (default: public URL host)")
	flags.BoolVar(&options.existsOK, "exists-ok", false, "exit successfully without changes when DIR is already initialized")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "cuelight-control init: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	options.hostnames, options.ips = hostnames, ips
	layout, err := initialize(options, time.Now())
	if errors.Is(err, errAlreadyInitialized) && options.existsOK {
		fmt.Fprintf(stdout, "cuelight-control init: %s is already initialized; nothing changed\n", options.dir)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-control init: %v\n", err)
		var usage *initUsageError
		if errors.As(err, &usage) {
			return 2
		}
		return 1
	}
	printNextSteps(stdout, layout, options)
	return 0
}

type initUsageError struct{ reason string }

func (err *initUsageError) Error() string { return err.reason }

var errAlreadyInitialized = errors.New("already initialized")

func initialize(options initOptions, now time.Time) (initLayout, error) {
	if strings.TrimSpace(options.dir) == "" {
		return initLayout{}, &initUsageError{"--dir is required"}
	}
	dir, err := filepath.Abs(options.dir)
	if err != nil {
		return initLayout{}, err
	}
	layout := newInitLayout(dir)
	if (options.tlsCert == "") != (options.tlsKey == "") {
		return initLayout{}, &initUsageError{"--tls-cert and --tls-key must be given together"}
	}
	external := options.tlsCert != ""
	if external && (len(options.hostnames) != 0 || len(options.ips) != 0) {
		return initLayout{}, &initUsageError{"--hostname/--ip only apply to a generated certificate; omit them with --tls-cert"}
	}
	if options.publicURL != "" {
		parsed, err := url.Parse(options.publicURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return initLayout{}, &initUsageError{"--public-url must be an absolute https:// URL without credentials, query, or fragment"}
		}
	}
	if _, _, err := net.SplitHostPort(options.listen); err != nil {
		return initLayout{}, &initUsageError{"--listen must be a host:port address"}
	}
	var names []string
	if !external {
		names, err = serverNames(options)
		if err != nil {
			return initLayout{}, err
		}
	} else {
		if layout.serverCert, err = filepath.Abs(options.tlsCert); err != nil {
			return initLayout{}, err
		}
		if layout.serverKey, err = filepath.Abs(options.tlsKey); err != nil {
			return initLayout{}, err
		}
		if _, err := tls.LoadX509KeyPair(layout.serverCert, layout.serverKey); err != nil {
			return initLayout{}, fmt.Errorf("load --tls-cert/--tls-key: %w", err)
		}
	}
	for _, value := range []string{layout.dir, layout.serverCert, layout.serverKey, options.listen, options.databaseURL, options.publicURL, options.networkName} {
		if strings.ContainsAny(value, "\"\\$`\r\n") {
			return initLayout{}, &initUsageError{fmt.Sprintf("%q contains a quote, backslash, $, backtick, or newline, which the env file cannot carry", value)}
		}
	}

	if _, err := os.Lstat(layout.env); err == nil {
		return initLayout{}, fmt.Errorf("%w: %s exists", errAlreadyInitialized, layout.env)
	}
	targets := []string{layout.env, layout.pki, layout.cas, layout.tmp}
	if !external {
		targets = append(targets, layout.tls)
	}
	for _, path := range targets {
		if _, err := os.Lstat(path); err == nil {
			return initLayout{}, fmt.Errorf("refusing to overwrite existing %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return initLayout{}, err
		}
	}

	files, err := generateMaterial(layout, names, now)
	if err != nil {
		return initLayout{}, err
	}
	if err := os.MkdirAll(layout.dir, 0o700); err != nil {
		return initLayout{}, err
	}
	directories := []string{layout.pki, layout.cas, layout.tmp}
	if !external {
		directories = append(directories, layout.tls)
	}
	for _, directory := range directories {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return initLayout{}, err
		}
	}
	files = append(files, generatedFile{layout.env, envFile(layout, options, now)})
	for _, file := range files {
		if err := writeExclusive(file.path, file.data); err != nil {
			return initLayout{}, err
		}
	}
	return layout, nil
}

func serverNames(options initOptions) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			names = append(names, name)
		}
	}
	for _, name := range options.hostnames {
		if net.ParseIP(name) != nil {
			return nil, &initUsageError{fmt.Sprintf("--hostname %q is an IP address; use --ip", name)}
		}
		add(name)
	}
	for _, address := range options.ips {
		if net.ParseIP(address) == nil {
			return nil, &initUsageError{fmt.Sprintf("--ip %q is not an IP address", address)}
		}
		add(address)
	}
	if options.publicURL != "" {
		if parsed, err := url.Parse(options.publicURL); err == nil {
			add(parsed.Hostname())
		}
	}
	if len(names) == 0 {
		return nil, &initUsageError{"give at least one --hostname or --ip (or --public-url) for the TLS certificate, or --tls-cert/--tls-key"}
	}
	return names, nil
}

type generatedFile struct {
	path string
	data []byte
}

func generateMaterial(layout initLayout, names []string, now time.Time) ([]generatedFile, error) {
	clock := fixedClock(now)
	serials := pki.CryptoSerialSource{Reader: rand.Reader}
	label := "Cuelight"
	if len(names) != 0 {
		label = "Cuelight " + names[0]
	}
	authority, err := pki.BootstrapAuthority(context.Background(), clock, serials, pki.AuthorityBootstrapOptions{
		CommonName: label + " node CA", Lifetime: nodeCALifetime, Backdate: certificateBackdate, Random: rand.Reader,
	})
	if err != nil {
		return nil, fmt.Errorf("generate node CA: %w", err)
	}
	_, publisher, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	publisherPEM, err := pkcs8PEM(publisher)
	if err != nil {
		return nil, err
	}
	seal, err := sealKey()
	if err != nil {
		return nil, err
	}
	files := []generatedFile{
		{layout.nodeCA, []byte(authority.Certificate)},
		{layout.nodeCAKey, []byte(authority.PrivateKey)},
		{layout.publisherKey, publisherPEM},
		{layout.sealKey, seal},
	}
	if len(names) == 0 {
		return files, nil
	}
	server, err := generateServerTLS(label, names, now)
	if err != nil {
		return nil, err
	}
	return append(files,
		generatedFile{layout.serverRoot, server.rootPEM},
		generatedFile{layout.serverRootKey, server.rootKeyPEM},
		// The chain file carries the root last so control can derive the join
		// link's ca=sha256: pin from it.
		generatedFile{layout.serverCert, append(append([]byte{}, server.leafPEM...), server.rootPEM...)},
		generatedFile{layout.serverKey, server.leafKeyPEM},
	), nil
}

type serverTLS struct {
	rootPEM, rootKeyPEM, leafPEM, leafKeyPEM []byte
}

// generateServerTLS issues an ECDSA P-256 root and leaf; Chromium (browsers,
// Electron) rejects the Ed25519 certificates used for node identities.
func generateServerTLS(label string, names []string, now time.Time) (serverTLS, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return serverTLS{}, err
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: label + " server root", Organization: []string{"Cuelight"}},
		NotBefore:             now.Add(-certificateBackdate),
		NotAfter:              now.Add(serverRootLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		return serverTLS{}, fmt.Errorf("create server root: %w", err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return serverTLS{}, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return serverTLS{}, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             now.Add(-certificateBackdate),
		NotAfter:              now.Add(serverLeafLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, name := range names {
		if address := net.ParseIP(name); address != nil {
			leafTemplate.IPAddresses = append(leafTemplate.IPAddresses, address)
		} else {
			leafTemplate.DNSNames = append(leafTemplate.DNSNames, name)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		return serverTLS{}, fmt.Errorf("create server certificate: %w", err)
	}
	rootKeyPEM, err := pkcs8PEM(rootKey)
	if err != nil {
		return serverTLS{}, err
	}
	leafKeyPEM, err := pkcs8PEM(leafKey)
	if err != nil {
		return serverTLS{}, err
	}
	return serverTLS{
		rootPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		rootKeyPEM: rootKeyPEM,
		leafPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		leafKeyPEM: leafKeyPEM,
	}, nil
}

func randomSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return serial.Add(serial, big.NewInt(1))
}

func pkcs8PEM(key any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// sealKey returns 32 random bytes that survive the loader's whitespace trim.
func sealKey() ([]byte, error) {
	for {
		key := make([]byte, pki.AESGCMKeySize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(key)) == len(key) {
			return key, nil
		}
	}
}

type fixedClock time.Time

func (clock fixedClock) Now() time.Time { return time.Time(clock) }

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// envValue double-quotes values with characters outside a shell-inert set;
// both systemd EnvironmentFile= and POSIX sh strip the quotes. Values never
// contain ", \, $, ` or newlines (rejected in initialize).
func envValue(value string) string {
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_./:@%+,=-", character)) {
			return `"` + value + `"`
		}
	}
	return value
}

func envFile(layout initLayout, options initOptions, now time.Time) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "# Generated by %s init on %s.\n", buildinfo.String("cuelight-control"), now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&out, "# Load with systemd EnvironmentFile= or: set -a; . %s; set +a\n", layout.env)
	line := func(name, value string) { fmt.Fprintf(&out, "%s=%s\n", name, envValue(value)) }
	line("CUELIGHT_CONTROL_LISTEN_ADDR", options.listen)
	if options.databaseURL != "" {
		line("CUELIGHT_CONTROL_DATABASE_URL", options.databaseURL)
	} else {
		out.WriteString("# Required: set the PostgreSQL URL, e.g.\n# CUELIGHT_CONTROL_DATABASE_URL=postgres://cuelight:PASSWORD@127.0.0.1:5432/cuelight?sslmode=disable\n")
	}
	if options.publicURL != "" {
		line("CUELIGHT_CONTROL_PUBLIC_URL", options.publicURL)
	} else {
		out.WriteString("# Recommended (pairing and join links): CUELIGHT_CONTROL_PUBLIC_URL=https://HOST:PORT\n")
	}
	if options.networkName != "" {
		line("CUELIGHT_CONTROL_NETWORK_NAME", options.networkName)
	}
	out.WriteString("# Without internet access, disable the release feed and use `cuelight-control updates import DIR`:\n# CUELIGHT_CONTROL_UPDATE_CHECK=off\n")
	line("CUELIGHT_CONTROL_TLS_CERT_PATH", layout.serverCert)
	line("CUELIGHT_CONTROL_TLS_KEY_PATH", layout.serverKey)
	line("CUELIGHT_CONTROL_CA_PATH", layout.nodeCA)
	line("CUELIGHT_CONTROL_CA_KEY_PATH", layout.nodeCAKey)
	line("CUELIGHT_CONTROL_PUBLISHER_KEY_PATH", layout.publisherKey)
	line("CUELIGHT_CONTROL_PUBLISHER_KEY_ID", "cuelight-publisher")
	line("CUELIGHT_CONTROL_SEAL_KEY_PATH", layout.sealKey)
	line("CUELIGHT_CONTROL_CAS_PATH", layout.cas)
	line("CUELIGHT_CONTROL_TEMP_ROOT", layout.tmp)
	return []byte(out.String())
}

func printNextSteps(out io.Writer, layout initLayout, options initOptions) {
	fmt.Fprintf(out, "Initialized %s\n  env file:  %s\n  TLS chain: %s\n", layout.dir, layout.env, layout.serverCert)
	if options.tlsCert == "" {
		if data, err := os.ReadFile(layout.serverRoot); err == nil {
			if block, _ := pem.Decode(data); block != nil {
				sum := sha256.Sum256(block.Bytes)
				fmt.Fprintf(out, "  TLS root:  %s (sha256:%s)\n", layout.serverRoot, hex.EncodeToString(sum[:]))
			}
		}
	}
	fmt.Fprintln(out, "\nNext steps:")
	step := 1
	if options.databaseURL == "" {
		fmt.Fprintf(out, "  %d. Set CUELIGHT_CONTROL_DATABASE_URL in %s (PostgreSQL 17 is the tested version).\n", step, layout.env)
		step++
	}
	fmt.Fprintf(out, "  %d. Start the server:  set -a; . %s; set +a; cuelight-control serve\n", step, layout.env)
	step++
	fmt.Fprintf(out, "  %d. Create an operator token:  (same env) cuelight-control admin token create --scopes security_admin,node_enroll,node_read,node_write,assignment_read,assignment_write,media_read,media_write,local_media_read,release_read,release_write\n", step)
	step++
	if options.tlsCert == "" {
		fmt.Fprintf(out, "  %d. Trust %s on operator machines and pass it to `cuelight-player provision --ca-file` (or use a join link).\n", step, layout.serverRoot)
	}
}
