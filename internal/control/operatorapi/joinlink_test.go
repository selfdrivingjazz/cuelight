package operatorapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"
)

func testCertificate(t *testing.T, name string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: isCA, BasicConstraintsValid: true}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSelfSignedRootSHA256(t *testing.T) {
	root, rootKey, rootPEM := testCertificate(t, "root", true, nil, nil)
	_, _, leafPEM := testCertificate(t, "leaf", false, root, rootKey)
	_, _, selfLeafPEM := testCertificate(t, "self-leaf", false, nil, nil)
	digest := sha256.Sum256(root.Raw)
	want := hex.EncodeToString(digest[:])

	if got, err := SelfSignedRootSHA256(append(append([]byte{}, leafPEM...), rootPEM...)); err != nil || got != want {
		t.Fatalf("full chain = %q, %v; want root digest", got, err)
	}
	// A chain whose top was issued by someone else is publicly trusted: no pin.
	if got, err := SelfSignedRootSHA256(leafPEM); err != nil || got != "" {
		t.Fatalf("leaf-only chain = %q, %v; want no pin", got, err)
	}
	if got, err := SelfSignedRootSHA256(selfLeafPEM); err != nil || got == "" {
		t.Fatalf("self-signed leaf = %q, %v; want pin", got, err)
	}
	if _, err := SelfSignedRootSHA256([]byte("not pem")); err == nil {
		t.Fatal("empty chain accepted")
	}
}

func TestJoinLinkURL(t *testing.T) {
	links, err := NewJoinLinks("https://studio.example:8443/", "", nil)
	if err == nil {
		t.Fatal("missing chain accepted")
	}
	_, _, selfPEM := testCertificate(t, "self", true, nil, nil)
	links, err = NewJoinLinks("https://studio.example:8443/", "", selfPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(links.URL("secret+/="))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme != "cuelight" || parsed.Host != "join" || query.Get("v") != "1" || query.Get("url") != "https://studio.example:8443" || query.Get("token") != "secret+/=" || query.Get("name") != "studio.example" || len(query.Get("ca")) != len("sha256:")+64 {
		t.Fatalf("join link = %s", parsed)
	}
	if disabled, err := NewJoinLinks("", "Studio", selfPEM); err != nil || disabled.URL("secret") != "" {
		t.Fatalf("no public URL should disable links: %q, %v", disabled.URL("secret"), err)
	}
}
