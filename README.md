# fude-supermaven

[supermaven-nvim](https://github.com/supermaven-inc/supermaven-nvim)'s
completion, on the free tier, as a fude plugin. It runs `sm-agent`, sends
it the buffer and the cursor as they change, and shows its completion as
ghost text.

## Build

```sh
oku sync && oku allow   # Go, TinyGo and binaryen, as fude pins them
fude plugin build       # writes fude-supermaven.wasm
```

## Install

Add this table to fude's `config.toml` (`fude man config` covers it):

```toml
[[plugin]]
path = "/path/to/fude-supermaven/fude-supermaven.wasm"
capabilities = ["buffer.read", "buffer.write", "ui", "process"]
programs = ["sh"]
```

A relative `path` is read from the folder of `config.toml`, and `~` is not
expanded. After a rebuild, `:plugin reload` loads the new `.wasm`.

## Use

Run `fude` on a file of code. The first start downloads `sm-agent` to
`~/.supermaven/binary/v20/<platform>-<arch>`, or under
`$XDG_DATA_HOME/supermaven` when that is set. When the agent asks to be
activated, the plugin picks the free tier, as `:SupermavenUseFree` does.

In insert mode a suggestion shows dim at the cursor, with its other lines
below. `<Tab>` accepts it, or types a tab when there is none. Leaving
insert mode clears it.

## Check

```sh
gofmt -l .                       # prints nothing when formatted
GOOS=wasip1 GOARCH=wasm go vet .
```

## How it differs from supermaven-nvim

- Neovim polls the agent every 25ms for 5s after a change. This
  recomputes the completion as each line of the agent's answer arrives.
- `sm-agent` runs through `sh`, which downloads it when missing, so `sh`
  is the only program the plugin declares.
- No commands, Pro sign-in, partial accept or log. Errors show as
  messages.
- A key typed while the plugin handles an event waits up to half its 16ms
  budget. Past that the plugin is busy, and `<Tab>` types a tab.
