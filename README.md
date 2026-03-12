[English](README.md) | [繁體中文](README_TW.md)

# AutoTo

AutoTo is a local-first AI assistant project with a unified Python backend, browser UI, one-click installer flow, and multi-channel integration support.

With AutoTo, you can:

- run a local AI assistant with a browser-based UI
- manage API keys, models, and channel settings
- integrate LINE, Telegram, Slack, and other chat platforms
- schedule recurring tasks and automation workflows
- extend the system with Taiwan-localized tools such as weather, invoice, and stock utilities

## Highlights

### Unified architecture
- Single backend entrypoint: `backend/server.py`
- Browser UI served by the backend
- Default local UI entrypoint: `http://127.0.0.1:5678`
- Default config path: `~/.autoto/config.json`

### Localized capabilities
- Traditional Chinese defaults
- Taiwan-focused tools and examples
- LINE integration support

### Extensible design
- Multiple model providers
- Custom tool integration
- Built-in scheduling support
- Installer flow for macOS and Windows

## Quick Start

### One-line install (recommended)

macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/hokidev26/autoto/main/install_mac.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex
```

After installation, start AutoTo with:

```bash
autoto
```

Then open: `http://127.0.0.1:5678`

### Run from the repo

```bash
git clone https://github.com/hokidev26/autoto.git
cd autoto
python3 -m pip install -r backend/requirements.txt
./start.sh
```

## Configuration

You can configure AutoTo from the browser settings page or by editing:

`~/.autoto/config.json`

Example:

```json
{
  "provider": "groq",
  "apiKey": "YOUR_KEY",
  "model": "llama-3.3-70b-versatile",
  "customUrl": ""
}
```

## Documentation

- `README_TW.md` — Traditional Chinese overview
- `GET_STARTED.md` — onboarding guide
- `SETUP_GUIDE_TW.md` — deployment guide
- `LINE_INTEGRATION.md` — LINE integration guide
- `TW_TOOLS.md` — Taiwan-focused tools guide
- `PROJECT_STRUCTURE.md` — architecture notes
- `NOTICE` — third-party attribution details

## License

This project is released under the MIT License. See `LICENSE`.

For third-party attribution details, see `NOTICE`.
