package security

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestPolicyBlocksLoopback(t *testing.T) {
	_, err := (TargetPolicy{}).ValidateURL(context.Background(), "https://127.0.0.1/hook")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked loopback, got %v", err)
	}
}

func TestPolicyBlocksPrivateAddress(t *testing.T) {
	if _, err := (TargetPolicy{}).ValidateURL(context.Background(), "https://10.0.0.1/hook"); err == nil {
		t.Fatal("private target accepted")
	}
}

func TestPolicyAllowsHTTPOnlyForControlledMode(t *testing.T) {
	if _, err := (TargetPolicy{AllowPrivate: true}).ValidateURL(context.Background(), "http://127.0.0.1:8081/hook"); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyRejectsUnsupportedScheme(t *testing.T) {
	if _, err := (TargetPolicy{AllowPrivate: true}).ValidateURL(context.Background(), "ftp://127.0.0.1/file"); err == nil {
		t.Fatal("FTP target accepted")
	}
}

func TestPolicyRejectsCredentials(t *testing.T) {
	if _, err := (TargetPolicy{AllowPrivate: true}).ValidateURL(context.Background(), "http://user:pass@127.0.0.1/hook"); err == nil {
		t.Fatal("URL credentials accepted")
	}
}

func TestMaskHeaders(t *testing.T) {
	headers := http.Header{"Authorization": {"Bearer private"}, "Cookie": {"session=private"}, "X-Webhook-Signature": {"sha256=private"}, "Content-Type": {"application/json"}}
	masked := MaskHeaders(headers)
	if masked["Authorization"] != "[REDACTED]" || masked["Cookie"] != "[REDACTED]" || masked["X-Webhook-Signature"] != "[REDACTED]" {
		t.Fatal("sensitive header was not masked")
	}
	if masked["Content-Type"] != "application/json" {
		t.Fatal("safe header was unexpectedly masked")
	}
}

func TestClientRejectsRedirects(t *testing.T) {
	client := (TargetPolicy{AllowPrivate: true}).Client(0)
	request, _ := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	if err := client.CheckRedirect(request, nil); err != http.ErrUseLastResponse {
		t.Fatalf("unexpected redirect policy: %v", err)
	}
}
