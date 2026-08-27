package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	playerclient "github.com/jmill/cable/internal/player/client"
)

const provisionTokenEnvironment = "CABLE_PLAYER_PROVISION_TOKEN"

type provisionedNode struct {
	Version                int    `json:"version"`
	NodeID                 string `json:"nodeId"`
	NodeName               string `json:"nodeName"`
	ControlURL             string `json:"controlUrl"`
	EventEpoch             string `json:"eventEpoch"`
	PublisherKeyID         string `json:"publisherKeyId"`
	CAPath                 string `json:"caPath"`
	CertificatePath        string `json:"certificatePath"`
	PrivateKeyPath         string `json:"privateKeyPath"`
	PublisherPublicKeyPath string `json:"publisherPublicKeyPath"`
}

func runProvision(ctx context.Context, args []string, lookup func(string) (string, bool), output io.Writer) error {
	flags := flag.NewFlagSet("cable-player provision", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	controlURL := flags.String("control-url", "", "controller API base URL")
	caFile := flags.String("ca-file", "", "controller CA certificate")
	nodeName := flags.String("node-name", "", "node display name")
	outputDirectory := flags.String("output", "", "private node state directory")
	recoverNode := flags.Bool("recover", false, "use a recovery token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*controlURL) == "" || strings.TrimSpace(*caFile) == "" || strings.TrimSpace(*outputDirectory) == "" {
		return errors.New("usage: cable-player provision --control-url URL --ca-file FILE --output DIRECTORY [--node-name NAME] [--recover]")
	}
	token, ok := lookup(provisionTokenEnvironment)
	if !ok || strings.TrimSpace(token) == "" {
		return fmt.Errorf("%s is required", provisionTokenEnvironment)
	}
	caPEM, err := os.ReadFile(*caFile)
	if err != nil {
		return fmt.Errorf("read controller CA: %w", err)
	}
	directory, err := filepath.Abs(*outputDirectory)
	if err != nil {
		return err
	}
	metadataPath := filepath.Join(directory, "node.json")
	if !*recoverNode {
		if _, err := os.Stat(metadataPath); err == nil {
			return errors.New("node identity already exists; use recovery instead of enrolling again")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create node directory: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	privatePEM := encodePrivateKeyPEM(privateDER)
	client, err := playerclient.New(playerclient.Config{BaseURL: normalizeControllerAPIURL(*controlURL), ServerCAPEM: caPEM})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	idempotencyBytes := make([]byte, 24)
	if _, err := rand.Read(idempotencyBytes); err != nil {
		return err
	}
	request := playerclient.EnrollmentRequest{Token: token, PublicKey: base64.StdEncoding.EncodeToString(publicKey), Name: strings.TrimSpace(*nodeName)}
	var enrollment playerclient.Enrollment
	if *recoverNode {
		enrollment, err = client.Recover(ctx, request, hex.EncodeToString(idempotencyBytes))
	} else {
		enrollment, err = client.Enroll(ctx, request, hex.EncodeToString(idempotencyBytes))
	}
	if err != nil {
		return err
	}
	publisherKey, err := base64.StdEncoding.DecodeString(enrollment.PublisherPublicKey)
	if err != nil || len(publisherKey) != ed25519.PublicKeySize {
		return errors.New("controller returned an invalid publisher public key")
	}
	if _, err := tls.X509KeyPair([]byte(enrollment.Certificate), privatePEM); err != nil {
		return errors.New("controller returned a certificate that does not match the generated key")
	}
	caPath := filepath.Join(directory, "controller-ca.pem")
	certificatePath := filepath.Join(directory, "node.pem")
	privateKeyPath := filepath.Join(directory, "node-key.pem")
	publisherPath := filepath.Join(directory, "publisher-public-key")
	files := []struct {
		path    string
		content []byte
		mode    os.FileMode
	}{
		{caPath, caPEM, 0o600},
		{certificatePath, []byte(enrollment.Certificate), 0o600},
		{privateKeyPath, privatePEM, 0o600},
		{publisherPath, []byte(enrollment.PublisherPublicKey + "\n"), 0o600},
	}
	for _, file := range files {
		if err := atomicWrite(file.path, file.content, file.mode); err != nil {
			return err
		}
	}
	metadata := provisionedNode{
		Version: 1, NodeID: enrollment.NodeID, NodeName: strings.TrimSpace(*nodeName), ControlURL: normalizeControllerAPIURL(*controlURL),
		EventEpoch: enrollment.EventEpoch, PublisherKeyID: enrollment.PublisherKeyID, CAPath: caPath,
		CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath, PublisherPublicKeyPath: publisherPath,
	}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := atomicWrite(metadataPath, encoded, 0o600); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(metadata)
}

func encodePrivateKeyPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func normalizeControllerAPIURL(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if strings.HasSuffix(trimmed, "/api/v1") {
		return trimmed
	}
	return trimmed + "/api/v1"
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
