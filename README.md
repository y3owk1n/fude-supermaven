# fude-supermaven

[supermaven-nvim](https://github.com/supermaven-inc/supermaven-nvim)'s
completion, on the free tier, as a fude plugin. It runs `sm-agent`, sends
it the buffer and the cursor as they change, and shows its completion as
ghost text.

## Install

```sh
fude plugin add github.com/y3owk1n/fude-supermaven
```

It needs fude 0.4.0 or later. fude prints what the plugin asks for, and
once you answer y, installs it and adds this table to `config.toml`
(`fude man config` covers it):

```toml
[[plugin]]
repo = "github.com/y3owk1n/fude-supermaven"
capabilities = ["buffer.read", "buffer.write", "ui", "process"]
programs = ["sh"]
```

Add the filetypes it should skip to that table:

```toml
options = { ignore_filetypes = ["markdown", "text"] }
```

- `:set fude-supermaven.ignore_filetypes markdown` changes the list while
  fude runs.
- `:plugin update fude-supermaven` updates it to its newest release.
- `plugins.lock`, beside `config.toml`, pins the commit, so keep it with
  your config.

## Build from source

```sh
oku sync && oku allow   # Go, TinyGo and binaryen, as fude pins them
fude plugin build       # writes fude-supermaven.wasm
```

To run your build, point the table at the folder in place of `repo`.
`:plugin reload` builds it again after a change.

```toml
[[plugin]]
path = "~/code/fude-supermaven"
```

Pushing a tag `v*` runs the release workflow, which attaches the
`fude-supermaven.wasm` that `fude plugin add` installs.

## Use

Run `fude` on a file of code. The first start downloads `sm-agent` to
`~/.supermaven/binary/v20/<platform>-<arch>`, or under
`$XDG_DATA_HOME/supermaven` when that is set. When the agent asks to be
activated, the plugin picks the free tier, as `:SupermavenUseFree` does.

In insert mode a suggestion shows dim at the cursor, with its other lines
below. `<Tab>` accepts it, or types a tab when there is none. Leaving
insert mode clears it.

A file of an ignored filetype gets no suggestions, and the plugin never
sends its text to `sm-agent`.

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
