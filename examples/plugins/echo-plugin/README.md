# Echo Plugin

A minimal Autoto plugin: a dependency-free Go MCP stdio server that exposes one tool, `echo`, which returns the text you send it. Use it as the smallest working reference for writing your own plugin. The full authoring guide is at [`docs/PLUGINS.md`](../../../docs/PLUGINS.md).

The directory is a complete plugin root: `main.go` (the server), `autoto.plugin.json` (the manifest), and — after you build it — the executable the manifest points at.

## Build

The manifest's `command` must point at a regular file inside this directory, so build the binary here.

Windows:

```powershell
go build -o echo-plugin.exe .
```

macOS / Linux:

```bash
go build -o echo-plugin .
```

On macOS/Linux, also change `"command"` in `autoto.plugin.json` from `echo-plugin.exe` to `echo-plugin`. If you rename the command after the plugin is already installed, run the plugin's update action (or reinstall) so Autoto adopts the changed manifest.

## Install in Autoto

1. Open Settings → Skills → plugins tab.
2. Enter this directory's absolute path as the plugin root and install. Plugins always install disabled; installation only reads the manifest and does not run anything.
3. Enable the plugin. Enabling executes local code, so Autoto asks for explicit confirmation.
4. Discovery runs on enable and the tool appears as `plugin__echo-plugin__echo` (the discover action re-lists it at any time).

The agent can now call the tool. Plugin calls are exec-risk, so depending on your permission mode the agent asks for approval first; each call spawns a fresh plugin process that exits when the call completes.

## Verify from the command line (optional)

The server is plain JSON-RPC 2.0 over stdin/stdout, so you can drive it without Autoto. PowerShell:

```powershell
@'
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}
'@ | .\echo-plugin.exe
```

Expected: an `initialize` result, a `tools/list` result containing the `echo` tool, and a `tools/call` result whose text content is `echo: hi`. The process exits cleanly when stdin closes.
