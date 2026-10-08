package release

import "testing"

func webPayload(url string) PayloadV1 {
	return PayloadV1{
		SchemaVersion:          SchemaVersion,
		MinimumProtocolVersion: ProtocolV1,
		Presentation:           Presentation{Kind: PresentationWeb, Web: &WebPresentation{URL: url}},
		RequiredFeatures:       []string{"chromium"},
	}
}

func TestWebPresentationValidation(t *testing.T) {
	if _, _, err := Digest(webPayload("https://example.com/app")); err != nil {
		t.Fatalf("valid web release rejected: %v", err)
	}
	for _, bad := range []string{"", "example.com", "ftp://example.com", "javascript:alert(1)", "https://user:pw@example.com/", "/relative"} {
		if _, _, err := Digest(webPayload(bad)); err == nil {
			t.Fatalf("url %q accepted", bad)
		}
	}
	withArtifact := webPayload("https://example.com/")
	withArtifact.Artifacts = []Artifact{{Digest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Size: 1, MediaType: "text/html"}}
	if _, _, err := Digest(withArtifact); err == nil {
		t.Fatal("web release with artifacts accepted")
	}
}

func TestWebPresentationDeliveryIsStreamOnly(t *testing.T) {
	payload := webPayload("https://example.com/")
	if !payload.AllowsDeliveryMode("stream") || payload.AllowsDeliveryMode("cache_then_play") {
		t.Fatal("web delivery must be stream only")
	}
	web, ok := payload.WebPresentation()
	if !ok || web.URL != "https://example.com/" {
		t.Fatalf("WebPresentation() = %v %v", web, ok)
	}
}
