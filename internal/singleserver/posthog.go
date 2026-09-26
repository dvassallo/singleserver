package singleserver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	vectorVersion              = "0.58.0"
	vectorServiceName          = "vector-singleserver.service"
	defaultPostHogLogsEndpoint = "https://us.i.posthog.com/i/v1/logs"
)

type LogShipState struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

func logShipStatePath() string {
	return filepath.Join(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"), "posthog.json")
}

func vectorConfigPath() string {
	return filepath.Join(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"), "vector.yaml")
}

func vectorDataDir() string {
	return envDefault("SINGLESERVER_VECTOR_DATA_DIR", "/var/lib/singleserver/vector")
}

func vectorBinPath() string {
	return envDefault("SINGLESERVER_VECTOR_BIN", "/usr/local/bin/vector")
}

func vectorUnitPath() string {
	return filepath.Join(envDefault("SINGLESERVER_SYSTEMD_DIR", "/etc/systemd/system"), vectorServiceName)
}

func loadLogShipState() (*LogShipState, error) {
	body, err := os.ReadFile(logShipStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state LogShipState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeLogShipState(state *LogShipState) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(logShipStatePath(), append(body, '\n'))
}

func cliPostHogConnect(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, posthogFlagTakesValue)
	if err != nil {
		return err
	}
	return withCLIMode(mode, func() error {
		return connectPostHog(args, w)
	})
}

func posthogFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	return name == "token" || name == "endpoint"
}

const posthogConnectUsage = "usage: singleserver connect posthog [--token <token>] [--endpoint <url>] [--disconnect]"

func connectPostHog(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("connect posthog", flag.ContinueOnError)
	fs.SetOutput(w)
	token := fs.String("token", "", "PostHog project token")
	endpoint := fs.String("endpoint", "", "OTLP/HTTP logs endpoint")
	disconnect := fs.Bool("disconnect", false, "stop shipping logs and remove the config")
	if err := fs.Parse(normalizeFlagArgs(args, posthogFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(posthogConnectUsage)
	}
	if *disconnect {
		return disconnectPostHog(w)
	}
	state, err := loadLogShipState()
	if err != nil {
		return err
	}
	if state == nil {
		state = &LogShipState{}
	}
	if value := strings.TrimSpace(*token); value != "" {
		state.Token = value
	}
	if value := strings.TrimSpace(*endpoint); value != "" {
		state.Endpoint = value
	}
	if state.Endpoint == "" {
		state.Endpoint = defaultPostHogLogsEndpoint
	}
	if state.Token == "" {
		return errors.New(posthogConnectUsage)
	}
	if !strings.HasPrefix(state.Endpoint, "https://") && !strings.HasPrefix(state.Endpoint, "http://") {
		return fmt.Errorf("--endpoint must be an http(s) URL, got %q", state.Endpoint)
	}

	if err := installVectorFunc(vectorBinPath()); err != nil {
		return err
	}
	writeCheck(w, "posthog", "vector", "ok", vectorVersion, vectorBinPath())

	if err := os.MkdirAll(vectorDataDir(), 0700); err != nil {
		return err
	}
	if err := writeFileAtomic(vectorConfigPath(), []byte(renderVectorConfig(state, vectorDataDir()))); err != nil {
		return err
	}
	if err := commandRunFunc(30*time.Second, vectorBinPath(), "validate", "--no-environment", vectorConfigPath()); err != nil {
		return err
	}
	writeCheck(w, "posthog", "config", "ok", vectorConfigPath())

	if err := writeLogShipState(state); err != nil {
		return err
	}
	if err := writeVectorService(); err != nil {
		return err
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "enable", vectorServiceName); err != nil {
		return err
	}
	if err := commandRunFunc(30*time.Second, "systemctl", "restart", vectorServiceName); err != nil {
		return err
	}
	writeCheck(w, "posthog", "logs", "ok", vectorServiceName, "shipping app and deploy logs to "+state.Endpoint)
	return nil
}

func disconnectPostHog(w io.Writer) error {
	if _, err := os.Stat(vectorUnitPath()); err == nil {
		if err := commandRunFunc(30*time.Second, "systemctl", "disable", "--now", vectorServiceName); err != nil {
			return err
		}
	}
	for _, path := range []string{vectorUnitPath(), vectorConfigPath(), logShipStatePath()} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	writeCheck(w, "posthog", "logs", "disabled", vectorServiceName)
	return nil
}

func doctorPostHog(w io.Writer) bool {
	state, err := loadLogShipState()
	if err != nil {
		writeCheck(w, "posthog", "logs", "failed", err.Error())
		return false
	}
	if state == nil {
		writeCheck(w, "posthog", "logs", "disabled", "-", "run singleserver connect posthog --token <token> to ship logs")
		return true
	}
	active, _ := commandOutputFunc(5*time.Second, "systemctl", "is-active", vectorServiceName)
	if strings.TrimSpace(active) != "active" {
		writeCheck(w, "posthog", "logs", "failed", vectorServiceName, "state="+valueOrDash(strings.TrimSpace(active)), "run singleserver connect posthog")
		return false
	}
	writeCheck(w, "posthog", "logs", "ok", vectorServiceName, state.Endpoint)
	return true
}

func writeVectorService() error {
	body := fmt.Sprintf(`[Unit]
Description=Single Server log shipping to PostHog
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s --config %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, vectorBinPath(), vectorConfigPath())
	if err := os.MkdirAll(filepath.Dir(vectorUnitPath()), 0755); err != nil {
		return err
	}
	return os.WriteFile(vectorUnitPath(), []byte(body), 0644)
}

// Vector interpolates ${VAR} in config values, so a literal $ is written as $$.
func vectorYAMLString(value string) string {
	return strconv.Quote(strings.ReplaceAll(value, "$", "$$"))
}

func renderVectorConfig(state *LogShipState, dataDir string) string {
	return fmt.Sprintf(`# Generated by singleserver connect posthog. Changes are overwritten.
data_dir: %s
sources:
  journal:
    type: journald
    include_units: [docker.service, singleserver.service]
    since_now: true
transforms:
  otlp:
    type: remap
    inputs: [journal]
    source: |
%s
sinks:
  posthog:
    type: opentelemetry
    inputs: [otlp]
    protocol:
      type: http
      uri: %s
      method: post
      auth:
        strategy: bearer
        token: %s
      encoding:
        codec: otlp
      batch:
        timeout_secs: 5
`, vectorYAMLString(dataDir), indentLines(vectorRemapSource, "      "), vectorYAMLString(state.Endpoint), vectorYAMLString(state.Token))
}

func indentLines(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// Maps a journald entry to one OTLP log record: app containers are named by their
// journald tag, the deploy daemon is "singleserver", and JSON lines (winston, pino) are unpacked.
const vectorRemapSource = `unit = string(._SYSTEMD_UNIT) ?? ""
tag = string(.CONTAINER_TAG) ?? ""
if unit == "docker.service" && tag == "" { abort }
service = if tag != "" { tag } else { "singleserver" }
raw = string(.message) ?? ""
priority = to_int(.PRIORITY) ?? 6
severity = if priority <= 3 { "ERROR" } else if priority == 4 { "WARN" } else if priority >= 7 { "DEBUG" } else { "INFO" }
body = raw
attributes = []
parsed, err = parse_json(raw)
if err == null && is_object(parsed) {
  fields = object!(parsed)
  level = fields.severity || fields.level
  if is_string(level) { severity = upcase!(level) }
  if is_integer(level) {
    n = int!(level)
    severity = if n >= 60 { "FATAL" } else if n >= 50 { "ERROR" } else if n >= 40 { "WARN" } else if n >= 30 { "INFO" } else if n >= 20 { "DEBUG" } else { "TRACE" }
  }
  message = if exists(fields.message) { fields.message } else { fields.msg }
  if message != null {
    body = if is_string(message) { string!(message) } else { encode_json(message) }
  }
  for_each(["message", "msg", "severity", "level", "timestamp", "time"]) -> |_index, key| {
    fields = remove!(fields, [key])
  }
  for_each(fields) -> |key, value| {
    text = if is_string(value) { string!(value) } else { encode_json(value) }
    attributes = push(attributes, {"key": key, "value": {"stringValue": text}})
  }
}
if severity == "WARNING" { severity = "WARN" }
if severity == "CRITICAL" || severity == "PANIC" { severity = "FATAL" }
numbers = {"TRACE": 1, "DEBUG": 5, "INFO": 9, "NOTICE": 10, "WARN": 13, "ERROR": 17, "FATAL": 21}
number = get(numbers, [severity]) ?? 9
container = string(.CONTAINER_NAME) ?? ""
if container != "" {
  attributes = push(attributes, {"key": "container.name", "value": {"stringValue": container}})
}
. = {"resourceLogs": [{
  "resource": {"attributes": [
    {"key": "service.name", "value": {"stringValue": service}},
    {"key": "host.name", "value": {"stringValue": get_hostname!()}}
  ]},
  "scopeLogs": [{
    "scope": {"name": "singleserver"},
    "logRecords": [{
      "timeUnixNano": to_unix_timestamp(timestamp(.timestamp) ?? now(), unit: "nanoseconds"),
      "observedTimeUnixNano": to_unix_timestamp(now(), unit: "nanoseconds"),
      "severityText": severity,
      "severityNumber": number,
      "body": {"stringValue": body},
      "attributes": attributes
    }]
  }]
}]}
`

var installVectorFunc = installVector

func installVector(binPath string) error {
	if out, err := commandOutputFunc(10*time.Second, binPath, "--version"); err == nil && strings.Contains(out, "vector "+vectorVersion+" ") {
		return nil
	}
	target, err := vectorTarget(runtime.GOARCH)
	if err != nil {
		return err
	}
	archive := fmt.Sprintf("vector-%s-%s.tar.gz", vectorVersion, target)
	base := fmt.Sprintf("https://github.com/vectordotdev/vector/releases/download/v%s/", vectorVersion)
	sums, err := httpGetBytes(base + fmt.Sprintf("vector-%s-SHA256SUMS", vectorVersion))
	if err != nil {
		return err
	}
	expected, err := checksumFor(string(sums), archive)
	if err != nil {
		return err
	}
	body, err := httpGetBytes(base + archive)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expected {
		return fmt.Errorf("checksum verification failed for %s", archive)
	}
	binary, err := extractVectorBinary(body)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return err
	}
	tmp := binPath + ".tmp"
	if err := os.WriteFile(tmp, binary, 0755); err != nil {
		return err
	}
	return os.Rename(tmp, binPath)
}

func vectorTarget(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64-unknown-linux-musl", nil
	case "arm64":
		return "aarch64-unknown-linux-musl", nil
	default:
		return "", fmt.Errorf("log shipping does not support architecture %s", goarch)
	}
}

func checksumFor(sums, file string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == file {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", file)
}

func extractVectorBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil, errors.New("vector binary not found in release archive")
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && strings.HasSuffix(header.Name, "/bin/vector") {
			return io.ReadAll(reader)
		}
	}
}

func httpGetBytes(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
