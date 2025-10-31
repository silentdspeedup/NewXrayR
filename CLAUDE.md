# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

XrayR is an Xray backend framework that supports multiple proxy protocols (V2ray, Trojan, Shadowsocks) and can interface with various panel types. It acts as a bridge between panel APIs and the Xray-core, managing nodes, users, traffic statistics, and certificate automation.

**Key characteristics:**
- Single instance can manage multiple panels and nodes simultaneously
- Configuration-driven with hot reload support (config changes trigger automatic restart)
- Built on top of Xray-core (github.com/xtls/xray-core)
- Supports modern features like VLESS, XTLS, and REALITY

## Build and Development Commands

### Building
```bash
# Standard build
go build -v -o XrayR -trimpath -ldflags "-s -w -buildid="

# With dependencies download
go mod download
go build -v -o XrayR -trimpath -ldflags "-s -w -buildid="

# Cross-platform build (example for Linux ARM64)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -v -o XrayR -trimpath -ldflags "-s -w -buildid="

# Windows build
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -v -o XrayR.exe -trimpath -ldflags "-s -w -buildid="
```

### Running
```bash
# Run with default config (./config.yml)
./XrayR

# Run with custom config
./XrayR --config /path/to/config.yml
./XrayR -c /path/to/config.yml

# Generate x25519 key pair for REALITY
./XrayR x25519

# Show version
./XrayR version
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests for specific package
go test ./api/sspanel
go test ./service/controller

# Run tests with verbose output
go test -v ./...

# Run specific test
go test -v ./service/controller -run TestControllerStart
```

### Docker
```bash
# Build Docker image
docker build -t xrayr .

# Run with Docker
docker run -v /path/to/config:/etc/XrayR xrayr
```

## Architecture Overview

### Execution Flow
1. **main.go** → **cmd.Execute()** → **cmd/root.go:run()**
2. **run()** reads config, creates Panel instance, watches for config changes
3. **Panel.Start()** (panel/panel.go):
   - Loads Xray core with DNS, routing, inbound/outbound configs
   - Initializes API clients based on PanelType
   - Creates Controller service for each node
   - Starts all services

### Component Responsibilities

**Panel** (`panel/panel.go`):
- Orchestrates the entire system
- Loads and configures Xray core instance
- Manages multiple Controller services (one per node)
- Handles hot reload on config file changes

**Controller** (`service/controller/controller.go`):
- Core service that manages a single node
- Communicates with panel API to fetch node info and user lists
- Dynamically adds/removes Xray inbound/outbound handlers
- Reports traffic statistics and online users back to panel
- Enforces speed limits, device limits, and audit rules
- Manages TLS certificates via mylego

**API Interface** (`api/api.go`):
- Defines common interface for all panel types
- Each panel type (sspanel, v2board, pmpanel, etc.) implements this interface
- Methods: GetNodeInfo, GetUserList, ReportNodeStatus, ReportUserTraffic, etc.

**Service Interface** (`service/service.go`):
- Simple Start()/Close() interface
- Controller implements this interface
- Allows Panel to manage multiple services uniformly

### Directory Structure

```
api/               - Panel API implementations (one subdirectory per panel type)
  ├─ api.go        - API interface definition
  ├─ apimodel.go   - Common data models (NodeInfo, UserInfo, etc.)
  ├─ sspanel/      - SSpanel implementation
  ├─ newV2board/   - V2board implementation
  └─ ...           - Other panel implementations

service/
  ├─ service.go    - Service interface
  └─ controller/   - Main controller logic
      ├─ controller.go      - Controller lifecycle and orchestration
      ├─ inboundbuilder.go  - Builds Xray inbound configs
      ├─ outboundbuilder.go - Builds Xray outbound configs
      ├─ userbuilder.go     - Builds user configs from API data

panel/
  ├─ panel.go      - Panel orchestration
  ├─ config.go     - Configuration structures
  └─ defaultConfig.go - Default configuration values

common/
  ├─ mylego/       - ACME certificate management
  ├─ limiter/      - Rate limiting (per-user and per-connection)
  ├─ rule/         - Audit rule matching
  └─ serverstatus/ - System stats collection

app/
  └─ mydispatcher/ - Custom dispatcher with traffic stats support

cmd/
  ├─ root.go       - Main command with config loading and hot reload
  ├─ version.go    - Version command
  └─ x25519.go     - Key generation for REALITY
```

## Configuration System

**Main Config** (`config.yml`):
- `Log`: Logging configuration
- `DnsConfigPath`: Path to dns.json
- `RouteConfigPath`: Path to route.json
- `InboundConfigPath`: Custom inbound configs
- `OutboundConfigPath`: Custom outbound configs
- `ConnectionConfig`: Connection policies (handshake timeout, idle timeout, buffer size)
- `Nodes`: Array of node configurations

**Per-Node Config**:
- `PanelType`: API implementation to use (SSpanel, NewV2board, PMpanel, etc.)
- `ApiConfig`: Panel API credentials and settings
- `ControllerConfig`: Node behavior (listen IP, update period, limits, certificates)

See `release/config/config.yml.example` for comprehensive examples.

## Key Concepts

### Multi-Node Support
A single XrayR instance can manage multiple nodes from multiple panels. Each node gets its own Controller service that runs independently with its own periodic tasks (node sync, user sync, traffic reporting).

### Hot Reload
The Panel watches config.yml. On change, it:
1. Calls Close() on all services
2. Triggers garbage collection
3. Re-parses config
4. Calls Start() to recreate everything

This happens automatically during runtime.

### Inbound/Outbound Management
The Controller dynamically adds/removes inbound and outbound handlers to the Xray core instance using the inbound.Manager and outbound.Manager features. Node info from the panel API determines the inbound config, and each user gets added as a user to that inbound.

### Traffic Accounting
Uses Xray's stats feature to track per-user traffic. The dispatcher (app/mydispatcher) collects stats and Controller periodically reports them back to the panel via the API.

### Certificate Management
The `common/mylego` package wraps the lego ACME client. Supports multiple methods:
- `dns`: DNS-01 challenge (supports many providers via lego)
- `http`: HTTP-01 challenge
- `tls`: TLS-ALPN-01 challenge
- `file`: Use existing cert/key files
- `none`: Disable TLS

Automatic renewal is handled by the Controller.

## Panel API Implementations

When adding support for a new panel:
1. Create new package under `api/` (e.g., `api/mypanel/`)
2. Implement the `api.API` interface
3. Define panel-specific models in `model.go`
4. Add panel initialization in `panel/panel.go` Start() method
5. Add test file following the pattern of other panel tests

Each API implementation translates between panel-specific formats and XrayR's common data models (NodeInfo, UserInfo, etc.).

## Common Development Tasks

### Adding Support for New Panel Type
1. Study existing implementations (e.g., `api/sspanel/`)
2. Create new directory `api/newpanel/`
3. Implement API interface with panel's REST API calls
4. Map panel's data structures to XrayR models
5. Register in `panel/panel.go` switch statement (line ~176)

### Modifying Node Behavior
Most node behavior is controlled in `service/controller/controller.go`:
- Periodic tasks are defined in Start() method
- User sync, node sync, traffic reporting all happen here
- Modify `ControllerConfig` struct to add new configuration options

### Debugging
- Set `Level: debug` in config.yml Log section to enable detailed logging with caller info
- Check Xray core logs for protocol-level issues
- API debug mode can be enabled per-panel (check Describe() method)

## Dependencies

Major dependencies:
- `github.com/xtls/xray-core` - The Xray core itself
- `github.com/spf13/cobra` - CLI framework
- `github.com/spf13/viper` - Configuration management
- `github.com/fsnotify/fsnotify` - File watching for hot reload
- `github.com/go-acme/lego/v4` - ACME/Let's Encrypt client
- `github.com/sirupsen/logrus` - Logging

## Testing Notes

- Most API packages have `_test.go` files with integration tests
- Tests often require actual panel API credentials (may be skipped in CI)
- Controller has unit tests for builder functions
- No mocking framework currently used; consider adding for better test coverage
