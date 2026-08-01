package grpcsvc

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/seed"
)

// Canonical supervisor environment variable names (openshell-core
// sandbox_env.rs) and the in-guest paths this driver materializes them at.
const (
	envEndpoint         = "OPENSHELL_ENDPOINT"
	envSandboxID        = "OPENSHELL_SANDBOX_ID"
	envSandboxName      = "OPENSHELL_SANDBOX"
	envSSHSocketPath    = "OPENSHELL_SSH_SOCKET_PATH"
	envSandboxCommand   = "OPENSHELL_SANDBOX_COMMAND"
	envLogLevel         = "OPENSHELL_LOG_LEVEL"
	envTelemetryEnabled = "OPENSHELL_TELEMETRY_ENABLED"
	envUserEnvironment  = "OPENSHELL_USER_ENVIRONMENT"
	envTLSCA            = "OPENSHELL_TLS_CA"
	envTLSCert          = "OPENSHELL_TLS_CERT"
	envTLSKey           = "OPENSHELL_TLS_KEY"
	envTokenFile        = "OPENSHELL_SANDBOX_TOKEN_FILE"

	guestTLSCA     = seed.GuestSeedDir + "/tls/ca.crt"
	guestTLSCert   = seed.GuestSeedDir + "/tls/tls.crt"
	guestTLSKey    = seed.GuestSeedDir + "/tls/tls.key"
	guestTokenFile = seed.GuestSeedDir + "/auth/sandbox.jwt"

	defaultSSHSocketPath = "/run/openshell/ssh.sock"
	defaultGuestPath     = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// sandboxEnv assembles the guest environment: base defaults, then user
// environment (template first, spec wins), then driver-owned keys, which
// always win. The supervisor requires the TLS trio for https endpoints and
// a token source; the raw token never enters the environment.
func sandboxEnv(cfg config.Config, sb *computev1.DriverSandbox, hasToken bool) (map[string]string, error) {
	spec := sb.GetSpec()
	tpl := spec.GetTemplate()

	env := map[string]string{
		"HOME": "/root",
		"PATH": defaultGuestPath,
		"TERM": "xterm",
	}

	user := make(map[string]string)
	for k, v := range tpl.GetEnvironment() {
		user[k] = v
	}
	for k, v := range spec.GetEnvironment() {
		user[k] = v
	}
	for k, v := range user {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("environment variable with empty name")
		}
		env[k] = v
	}
	if len(user) > 0 {
		blob, err := json.Marshal(user)
		if err != nil {
			return nil, fmt.Errorf("encode user environment: %w", err)
		}
		env[envUserEnvironment] = string(blob)
	}

	logLevel := spec.GetLogLevel()
	if logLevel == "" {
		logLevel = cfg.LogLevel
	}
	sshSocket := tpl.GetAgentSocketPath()
	if sshSocket == "" {
		sshSocket = defaultSSHSocketPath
	}

	env[envEndpoint] = cfg.GRPCEndpoint
	env[envSandboxID] = sb.GetId()
	env[envSandboxName] = sb.GetName()
	env[envSSHSocketPath] = sshSocket
	env[envSandboxCommand] = "sleep infinity"
	env[envLogLevel] = logLevel
	env[envTelemetryEnabled] = "false"
	env["PATH"] = defaultGuestPath // driver-owned even if user env set it

	if strings.HasPrefix(cfg.GRPCEndpoint, "https://") {
		env[envTLSCA] = guestTLSCA
		env[envTLSCert] = guestTLSCert
		env[envTLSKey] = guestTLSKey
	}
	if hasToken {
		env[envTokenFile] = guestTokenFile
	}
	return env, nil
}
