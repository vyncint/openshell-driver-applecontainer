package hostsetup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRenderLaunchAgent(t *testing.T) {
	plist := RenderLaunchAgent("/opt/homebrew/bin/openshell-driver-applecontainer",
		"/opt/homebrew/var/openshell/tls", "/Users/x/Library/Logs/driver.log")
	for _, want := range []string{
		"<string>" + AgentLabel + "</string>",
		"<string>/opt/homebrew/bin/openshell-driver-applecontainer</string>",
		"<key>OPENSHELL_LOCAL_TLS_DIR</key>",
		"<string>/opt/homebrew/var/openshell/tls</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"/opt/homebrew/bin:", // PATH for the container CLI under launchd
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestRenderLaunchAgentEscapesXML(t *testing.T) {
	plist := RenderLaunchAgent("/tmp/dir with <odd> & chars/bin", "/tls", "/log")
	if strings.Contains(plist, "<odd>") {
		t.Error("unescaped XML in plist")
	}
	if !strings.Contains(plist, "&lt;odd&gt; &amp; chars") {
		t.Errorf("expected escaped path, got:\n%s", plist)
	}
}

func TestUpsertManagedBlock(t *testing.T) {
	lines := GatewayEnvLines("/tmp/oshl-ac/driver.sock", "/tls")

	// Fresh file. Path values are single-quoted (the file is shell-sourced).
	fresh := UpsertManagedBlock("", lines)
	for _, want := range []string{
		blockBegin, blockEnd,
		"OPENSHELL_DRIVERS=applecontainer",
		"OPENSHELL_COMPUTE_DRIVER_SOCKET='/tmp/oshl-ac/driver.sock'",
		"OPENSHELL_BIND_ADDRESS=0.0.0.0",
		"OPENSHELL_ENABLE_MTLS_AUTH=true",
		"OPENSHELL_LOCAL_TLS_DIR='/tls'",
	} {
		if !strings.Contains(fresh, want) {
			t.Errorf("managed block missing %q", want)
		}
	}

	// Idempotent: re-upsert yields identical content.
	if again := UpsertManagedBlock(fresh, lines); again != fresh {
		t.Errorf("upsert not idempotent:\n%q\nvs\n%q", fresh, again)
	}

	// User content survives, block is replaced not duplicated.
	userFile := "# my notes\nOPENSHELL_LOG_JSON=true\n" + fresh
	updated := UpsertManagedBlock(userFile, GatewayEnvLines("/other.sock", "/tls2"))
	if !strings.Contains(updated, "# my notes") || !strings.Contains(updated, "OPENSHELL_LOG_JSON=true") {
		t.Error("user content lost")
	}
	if strings.Count(updated, blockBegin) != 1 {
		t.Error("managed block duplicated")
	}
	if !strings.Contains(updated, "/other.sock") || strings.Contains(updated, "/tmp/oshl-ac/driver.sock") {
		t.Error("managed block not replaced")
	}
}

func TestGatewayEnvShellQuoting(t *testing.T) {
	// A path with a space and a single quote must survive a shell source.
	lines := GatewayEnvLines("/tmp/oshl ac/it's.sock", "/tls dir")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `OPENSHELL_COMPUTE_DRIVER_SOCKET='/tmp/oshl ac/it'\''s.sock'`) {
		t.Errorf("socket not shell-quoted: %q", joined)
	}
	if !strings.Contains(joined, "OPENSHELL_LOCAL_TLS_DIR='/tls dir'") {
		t.Errorf("tls dir not shell-quoted: %q", joined)
	}
}

func TestRemoveManagedBlockDegenerate(t *testing.T) {
	lines := GatewayEnvLines("/s", "/t")
	one := UpsertManagedBlock("", lines)

	// Two managed blocks (external tampering): both removed.
	two := "KEEP=1\n" + one + one
	if got := RemoveManagedBlock(two); strings.Contains(got, blockBegin) {
		t.Errorf("duplicate blocks not fully removed: %q", got)
	} else if !strings.Contains(got, "KEEP=1") {
		t.Errorf("user content lost: %q", got)
	}

	// CRLF line endings: no stray carriage return left behind.
	crlf := "A=1\r\n" + blockBegin + "\r\n" + "X=1\r\n" + blockEnd + "\r\nB=2\r\n"
	got := RemoveManagedBlock(crlf)
	if strings.Contains(got, blockBegin) || strings.Contains(got, "X=1") {
		t.Errorf("CRLF block not removed: %q", got)
	}
	if strings.Contains(got, "\r\r") {
		t.Errorf("stray carriage return: %q", got)
	}

	// Damaged block (begin without end): dropped from begin onward.
	damaged := "A=1\n" + blockBegin + "\nX=1\n"
	if got := RemoveManagedBlock(damaged); strings.Contains(got, blockBegin) || strings.Contains(got, "X=1") {
		t.Errorf("damaged block not removed: %q", got)
	} else if !strings.Contains(got, "A=1") {
		t.Errorf("content before damaged block lost: %q", got)
	}

	// Upsert over a duplicated-block file converges to exactly one block.
	if got := UpsertManagedBlock(two, lines); strings.Count(got, blockBegin) != 1 {
		t.Errorf("upsert did not converge to one block: %d", strings.Count(got, blockBegin))
	}
}

func TestRemoveManagedBlock(t *testing.T) {
	lines := GatewayEnvLines("/s", "/t")
	full := UpsertManagedBlock("KEEP=1\n", lines)
	rest := RemoveManagedBlock(full)
	if strings.Contains(rest, blockBegin) || strings.Contains(rest, "OPENSHELL_DRIVERS") {
		t.Errorf("block not removed: %q", rest)
	}
	if !strings.Contains(rest, "KEEP=1") {
		t.Errorf("user content lost: %q", rest)
	}
	// Removing from a block-only file leaves nothing meaningful.
	if only := RemoveManagedBlock(UpsertManagedBlock("", lines)); strings.TrimSpace(only) != "" {
		t.Errorf("block-only file should empty out, got %q", only)
	}
	// No block: unchanged.
	if got := RemoveManagedBlock("A=1\n"); got != "A=1\n" {
		t.Errorf("no-block content changed: %q", got)
	}
}

// selfSignedCert makes a PEM certificate carrying the given IP SANs.
func selfSignedCert(t *testing.T, ips ...string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCertHasIPSAN(t *testing.T) {
	cert := selfSignedCert(t, "127.0.0.1", "192.168.65.1")
	if ok, err := CertHasIPSAN(cert, "192.168.65.1"); err != nil || !ok {
		t.Errorf("expected SAN present, got %v %v", ok, err)
	}
	if ok, err := CertHasIPSAN(cert, "192.168.66.1"); err != nil || ok {
		t.Errorf("expected SAN absent, got %v %v", ok, err)
	}
	if _, err := CertHasIPSAN([]byte("not a cert"), "1.2.3.4"); err == nil {
		t.Error("garbage certificate must error")
	}
	if _, err := CertHasIPSAN(cert, "not-an-ip"); err == nil {
		t.Error("garbage ip must error")
	}
}

func TestFixLocalGatewayMetadata(t *testing.T) {
	broken := []byte(`{
  "name": "openshell",
  "gateway_endpoint": "https://[::1]:17670",
  "is_remote": false,
  "gateway_port": 0,
  "auth_mode": "mtls"
}`)
	fixed, changed, err := FixLocalGatewayMetadata(broken)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var m map[string]any
	if err := json.Unmarshal(fixed, &m); err != nil {
		t.Fatal(err)
	}
	if m["gateway_endpoint"] != "https://127.0.0.1:17670" {
		t.Errorf("endpoint = %v", m["gateway_endpoint"])
	}
	if m["auth_mode"] != "mtls" || m["name"] != "openshell" {
		t.Errorf("other fields lost: %v", m)
	}

	// Already-good and remote registrations are untouched.
	for _, ok := range []string{
		`{"gateway_endpoint": "https://127.0.0.1:17670", "is_remote": false}`,
		`{"gateway_endpoint": "https://[::1]:17670", "is_remote": true}`,
		`{"gateway_endpoint": "https://gw.example.com:17670", "is_remote": false}`,
	} {
		if _, changed, err := FixLocalGatewayMetadata([]byte(ok)); err != nil || changed {
			t.Errorf("should not change %s (changed=%v err=%v)", ok, changed, err)
		}
	}
}
