# For agents

Use this site to learn the TypeSafe Go client, SysOneScript, Studio and semlint. The public GitHub Pages site serves static documentation. It does not execute code or host MCP or A2A tools.

## Read the documentation

1. Fetch [llms.txt](https://donaldmurillo.github.io/system-one-playground/llms.txt) for discovery and setup.
2. Read [the Markdown index](https://donaldmurillo.github.io/system-one-playground/llm-pages.md) to find relevant pages.
3. Fetch a page's explicit Markdown URL, such as [Jev enablement](https://donaldmurillo.github.io/system-one-playground/docs/enable-jev/llm.md), [the language reference](https://donaldmurillo.github.io/system-one-playground/docs/language/llm.md), or [the project CLI/MCP reference](https://donaldmurillo.github.io/system-one-playground/docs/agent-interface/llm.md).

Use the explicit `/llm.md` URLs: GitHub Pages does not provide dynamic content negotiation. The exported agent card is descriptive metadata with no callable interfaces. There is no live `/mcp` on this public site. Search and the sitemap provide additional discovery; no API key is required to read docs.

## Install and inspect a project

```sh
git clone https://github.com/DonaldMurillo/system-one-playground.git
cd system-one-playground
go build -o bin/sos ./cmd/sos
go build -o bin/sysone ./cmd/sysone
go build -o bin/sos-studio ./cmd/sos-studio
export PATH="$PWD/bin:$PATH"
sysone --project examples/sos/repo-assistant tree
sysone --project examples/sos/repo-assistant config main.sos
sysone --project examples/sos/repo-assistant check main.sos
```

Go 1.25+ is required. Keep the three binaries together. Global `--project` precedes the command. `tree`, `config` and canonical `check` do not make provider requests. Read the source and effective limits before running or analyzing unfamiliar code.

## Connect a local MCP client

Configure your agent's MCP client with absolute paths:

```json
{
  "mcpServers": {
    "sysone": {
      "command": "/absolute/path/system-one-playground/bin/sysone",
      "args": ["--project", "/absolute/path/your-project", "mcp"]
    }
  }
}
```

This starts the project service over **stdio**, not HTTP. It exposes file tools, settings, check, analyze, run and build. It operates on project files or explicitly supplied source, not an existing Studio window's unsaved buffer. See [tool schemas and behavior](/docs/agent-interface).

The separate Go documentation server exposes documentation MCP when run locally with `cd docs-site && go run .`. That service is for docs and is not a substitute for `sysone mcp`. It is absent from static deployment.

## A useful working sequence

- Inspect the project tree, read the relevant files and effective settings.
- Use `check` for local validation; `analyze`/`explain` may spend Jev requests.
- Use revision-aware writes: read first, then supply the returned revision when replacing a file. Reconcile conflicts rather than overwriting blindly.
- Run with appropriate request/time limits. Inspect output, errors, interpretation, traces and usage.
- Build from saved files. Test the resulting CLI; a build succeeding does not prove a judgment was appropriate.

For structured scripting without an MCP client, `sysone api run` accepts JSON on stdin. Provider credentials belong in the launching environment or project settings, not source or committed client configuration. Environment listing returns names/status only; secret writes can still be logged by the invoking agent client.

## Enable Jev deliberately

Follow [Add Jev to your application](/docs/enable-jev) for all enablement paths. Explicit judgments, semantic interpretation and editor assistance have separate controls. Execution may write files, run subprocesses or make paid requests. Budgets constrain requests and time, not account-wide monetary spend. Do not treat model confidence as authorization to perform an external action.
