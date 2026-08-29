# Model Context Protocol (MCP) Adapter Configuration

The `amc-mcp` adapter allows LLMs/agents to interact with the Agent Machine Control (AMC) daemon using the Model Context Protocol (MCP). It supports two communication modes: Stdio and Streamable HTTP.

## 1. Stdio Mode

Stdio mode is the default and runs the MCP server as a subprocess communicating over standard input/output.

To configure an agent/client to connect via stdio:

```json
{
  "mcpServers": {
    "agent-machine-control": {
      "command": "/usr/local/bin/amc-mcp",
      "args": ["--state-dir", "/path/to/amc/statedir"]
    }
  }
}
```

The adapter automatically reads `agent-mcp.token` from the state directory to authenticate itself to the AMC daemon as `agent:mcp-local`.

## 2. Streamable HTTP Mode (Local Loopback)

For local client environments where stdio subprocess execution is not ideal, you can launch the adapter in streamable HTTP mode by specifying the `--listen` flag.

The adapter utilizes the Model Context Protocol Go SDK version v1.7.0. It negotiates the following protocol versions:

- `2026-07-28` (using `server/discover` RPC stateless probe)
- `2025-11-25`
- `2025-06-18`
- `2025-03-26`
- `2024-11-05`

Under streamable HTTP mode, the SDK uses the Server-Sent Events (SSE) transport protocol for server-to-client events and standard HTTP POST requests for client-to-server messages.

### Server Startup

To start the streamable HTTP server, run:

```bash
amc-mcp --state-dir /path/to/amc/statedir --listen 127.0.0.1:8080
```

> [!IMPORTANT]
> The server validates that the `--listen` address is a loopback IP literal (specifically `127.0.0.1` or `[::1]`). It explicitly rejects hostnames like `localhost` and binds only to valid IP literals to prevent dns-rebinding and unauthorized external access.

### Client Authentication

HTTP requests must include the token found in `/path/to/amc/statedir/auth/agent-mcp.token` as a bearer token in the `Authorization` header:

```http
Authorization: Bearer <token>
Accept: application/json, text/event-stream
```

> [!NOTE]
> For browser safety, the adapter rejects all requests containing a non-empty `Origin` header.
