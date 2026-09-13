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
	envMainProcessSpec  = "OPENSHELL_MAIN_PROCESS_SPEC"
	envLogLevel         = "OPENSHELL_LOG_LEVEL"
	envTelemetryEnabled = "OPENSHELL_TELEMETRY_ENABLED"
	envNetworkRuntime   = "OPENSHELL_NETWORK_RUNTIME_CAPABILITIES"
	envUserEnvironment  = "OPENSHELL_USER_ENVIRONMENT"
	envTLSCA            = "OPENSHELL_TLS_CA"
	envTLSCert          = "OPENSHELL_TLS_CERT"
	envTLSKey           = "OPENSHELL_TLS_KEY"
	envTLSServerName    = "OPENSHELL_GATEWAY_TLS_SERVER_NAME"
	envToken            = "OPENSHELL_SANDBOX_TOKEN"
	envTokenFile        = "OPENSHELL_SANDBOX_TOKEN_FILE"
	envOCIImageUser     = "OPENSHELL_OCI_IMAGE_USER"
	envSandboxUID       = "OPENSHELL_SANDBOX_UID"
	envSandboxGID       = "OPENSHELL_SANDBOX_GID"

	guestTLSCA     = seed.GuestSeedDir + "/tls/ca.crt"
	guestTLSCert   = seed.GuestSeedDir + "/tls/tls.crt"
	guestTLSKey    = seed.GuestSeedDir + "/tls/tls.key"
	guestTokenFile = seed.GuestSeedDir + "/auth/sandbox.jwt"

	defaultSSHSocketPath = "/run/openshell/ssh.sock"
	defaultGuestPath     = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// mainProcessSpec is the versioned, lossless driver→supervisor transport for
// the sandbox's canonical process (openshell-core MainProcessConfig). The
// supervisor decodes it without shell parsing, so argument boundaries in
// `openshell sandbox create -- <command>` survive intact.
type mainProcessSpec struct {
	Version int      `json:"version"`
	Command []string `json:"command"`
	TTY     bool     `json:"tty"`
}

// mainProcessSpecVersion is MainProcessConfig::VERSION upstream.
const mainProcessSpecVersion = 1

// defaultMainProcess is MainProcessConfig::scratch(): what every upstream
// driver — and the supervisor itself when the variable is absent — uses for
// a sandbox created without a command.
func defaultMainProcess() mainProcessSpec {
	return mainProcessSpec{Version: mainProcessSpecVersion, Command: []string{"/bin/bash", "-l"}, TTY: true}
}

// mainProcessFromSpec mirrors MainProcessConfig::from_driver_spec: a spec
// with a command wins verbatim, anything else is the scratch default.
func mainProcessFromSpec(spec *computev1.DriverSandboxSpec) mainProcessSpec {
	if cmd := spec.GetCommand(); len(cmd) > 0 {
		return mainProcessSpec{Version: mainProcessSpecVersion, Command: append([]string(nil), cmd...), TTY: spec.GetTty()}
	}
	return defaultMainProcess()
}

// driverOwnedEnv lists the variables a user environment may never set: the
// supervisor's identity, transport and trust anchors. A template that
// smuggled OPENSHELL_GATEWAY_TLS_SERVER_NAME could redirect the supervisor
// to a certificate the sandbox author controls and intercept the sandbox
// JWT; OPENSHELL_SANDBOX_TOKEN(_FILE) would let it substitute credentials.
// Everything here is either overwritten by the driver below or removed.
var driverOwnedEnv = map[string]bool{
	envEndpoint: true, envSandboxID: true, envSandboxName: true, envSSHSocketPath: true,
	envMainProcessSpec: true, envLogLevel: true, envTelemetryEnabled: true, envNetworkRuntime: true,
	envUserEnvironment: true, envTLSCA: true, envTLSCert: true, envTLSKey: true, envTLSServerName: true,
	envToken: true, envTokenFile: true, envOCIImageUser: true, envSandboxUID: true, envSandboxGID: true,
	"PATH": true,
}

// sandboxEnv assembles the guest environment: base defaults, then user
// environment (template first, spec wins) with driver-owned names stripped,
// then driver-owned keys. The supervisor requires the TLS trio for https
// endpoints and a token source; the raw token never enters the environment.
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
		if strings.ContainsAny(k, "=\x00") || strings.Contains(v, "\x00") {
			return nil, fmt.Errorf("environment variable %q has an invalid name or value", k)
		}
		if driverOwnedEnv[k] {
			// Silently dropped rather than rejected: upstream drivers
			// overwrite/remove these too, and a template author gains
			// nothing from an error they cannot act on.
			delete(user, k)
			continue
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
	mainProcess, err := json.Marshal(mainProcessFromSpec(spec))
	if err != nil {
		return nil, fmt.Errorf("encode main process spec: %w", err)
	}

	env[envEndpoint] = cfg.GRPCEndpoint
	env[envSandboxID] = sb.GetId()
	env[envSandboxName] = sb.GetName()
	env[envSSHSocketPath] = sshSocket
	env[envMainProcessSpec] = string(mainProcess)
	env[envLogLevel] = logLevel
	env[envTelemetryEnabled] = "false"
	// Runtime networking capabilities are driver-owned. Like the upstream VM
	// driver, this one does not provide policy DNS / transparent TCP
	// interception substrate in the guest, so it claims none — the
	// supervisor then keeps its explicit-proxy enforcement.
	env[envNetworkRuntime] = ""
	// The "OCI user" identity contract (docker/podman parity): the raw image
	// USER is supplied, the numeric pair is explicitly empty, and the
	// supervisor resolves the workload identity from the image.
	env[envSandboxUID] = ""
	env[envSandboxGID] = ""

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
