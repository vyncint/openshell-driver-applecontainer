// Package hostsetup implements the one-time `setup` / `uninstall`
// subcommands: it wires the driver into launchd, points the stock OpenShell
// gateway service at the driver through its gateway.env hook, and makes the
// gateway reachable from guest VMs. Everything is idempotent so setup can
// be re-run at any time to repair the installation.
package hostsetup

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// AgentLabel is the launchd label of the driver service.
const AgentLabel = "local.openshell-driver-applecontainer"

// Markers delimit the managed block in the gateway.env file the Homebrew
// openshell service sources at startup. Everything between them is owned by
// setup; user content outside survives untouched.
const (
	blockBegin = "# >>> openshell-driver-applecontainer setup >>>"
	blockEnd   = "# <<< openshell-driver-applecontainer setup <<<"
)

// launchdPATH makes Homebrew-installed binaries (the `container` CLI)
// visible to the driver: launchd agents do not inherit a login shell PATH.
const launchdPATH = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderLaunchAgent produces the launchd plist that keeps the driver
// running: started at login, restarted on exit, logging to logPath.
func RenderLaunchAgent(binPath, tlsDir, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
		<key>OPENSHELL_LOCAL_TLS_DIR</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, AgentLabel, xmlEscape(binPath), launchdPATH, xmlEscape(tlsDir), xmlEscape(logPath), xmlEscape(logPath))
}

// GatewayEnvLines returns the managed settings the stock gateway service
// needs to select this driver and be reachable from guest VMs.
func GatewayEnvLines(socket, tlsDir string) []string {
	return []string{
		"OPENSHELL_DRIVERS=applecontainer",
		"OPENSHELL_COMPUTE_DRIVER_SOCKET=" + socket,
		"OPENSHELL_BIND_ADDRESS=0.0.0.0",
		"OPENSHELL_ENABLE_MTLS_AUTH=true",
		"OPENSHELL_LOCAL_TLS_DIR=" + tlsDir,
	}
}

// UpsertManagedBlock replaces (or appends) the managed marker block in a
// gateway.env file's content, leaving everything else untouched.
func UpsertManagedBlock(existing string, lines []string) string {
	body := blockBegin + "\n" + strings.Join(lines, "\n") + "\n" + blockEnd + "\n"
	stripped := RemoveManagedBlock(existing)
	if strings.TrimSpace(stripped) == "" {
		return body
	}
	if !strings.HasSuffix(stripped, "\n") {
		stripped += "\n"
	}
	return stripped + body
}

// RemoveManagedBlock strips the managed marker block (inclusive) from a
// gateway.env file's content.
func RemoveManagedBlock(existing string) string {
	begin := strings.Index(existing, blockBegin)
	if begin < 0 {
		return existing
	}
	end := strings.Index(existing[begin:], blockEnd)
	if end < 0 {
		// Damaged block: drop from the begin marker onward.
		return strings.TrimRight(existing[:begin], "\n") + "\n"
	}
	rest := existing[begin+end+len(blockEnd):]
	rest = strings.TrimPrefix(rest, "\n")
	head := existing[:begin]
	if strings.TrimSpace(head) == "" {
		return rest
	}
	return head + rest
}

// CertHasIPSAN reports whether the PEM certificate carries a subject
// alternative name for the given IP.
func CertHasIPSAN(certPEM []byte, ip string) (bool, error) {
	want := net.ParseIP(ip)
	if want == nil {
		return false, fmt.Errorf("invalid ip %q", ip)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false, fmt.Errorf("no PEM block in certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, fmt.Errorf("parse certificate: %w", err)
	}
	for _, san := range cert.IPAddresses {
		if san.Equal(want) {
			return true, nil
		}
	}
	return false, nil
}

// FixLocalGatewayMetadata rewrites a local (non-remote) CLI gateway
// registration that points at IPv6 loopback to IPv4 loopback: the gateway
// binds per bind_address (IPv4), so `https://[::1]:17670` — which older
// installers wrote — can never connect.
func FixLocalGatewayMetadata(data []byte) ([]byte, bool, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false, err
	}
	if remote, _ := m["is_remote"].(bool); remote {
		return data, false, nil
	}
	endpoint, _ := m["gateway_endpoint"].(string)
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() != "::1" {
		return data, false, nil
	}
	port := u.Port()
	if port == "" {
		port = "17670"
	}
	u.Host = net.JoinHostPort("127.0.0.1", port)
	m["gateway_endpoint"] = u.String()
	fixed, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return fixed, true, nil
}
