# Set up prereview (instructions for a coding agent)

You are a coding agent, and the user wants to review your changes in prereview.
Follow these steps in order, then read the skill you installed for how to run a
review. Every command here runs without a prompt, so none of them needs a
terminal.

## 1. Check for the binary

```sh
prereview --version
```

If that prints a version, go to step 3. If the command isn't found, go to
step 2.

## 2. Install the binary

On macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/livetemplate/prereview/main/install.sh | sh
```

It downloads the latest release, checks its checksum, and installs into a
folder you can write to (set `PREREVIEW_INSTALL_DIR` to choose one). If the
folder it reports isn't on your `PATH`, run the binary by its full path from
then on. On Windows, use `scoop bucket add prereview https://github.com/livetemplate/prereview`
and then `scoop install prereview/prereview`. With Go installed, you can also run
`go install github.com/livetemplate/prereview@latest`.

Run `prereview --version` again to confirm. Every install method, and behind a
proxy, is in [install.md](install.md).

## 3. Install the skill for your agent

Find yourself in this table and run the command in its row:

| You are | Command | Installed file |
|---|---|---|
| Claude Code | `prereview --install-skill --client=claude` | `~/.claude/skills/prereview/SKILL.md` |
| OpenAI Codex CLI | `prereview --install-skill --client=codex` | `~/.codex/skills/prereview/SKILL.md` |
| Gemini CLI | `prereview --install-skill --client=gemini` | `~/.gemini/commands/prereview.toml` |
| opencode | `prereview --install-skill --client=opencode` | `~/.config/opencode/commands/prereview.md` |
| aider | `prereview --install-skill --client=aider` | `~/.config/prereview/aider/prereview-aider.sh` |
| cursor-agent | `prereview --install-skill --client=cursor` | `~/.cursor/rules/prereview.mdc` |

Always pass `--client`. Without it, prereview shows a menu, and with no terminal
to answer it, it installs the Claude Code skill whatever agent you are. If you
aren't in the table, skip this step and use the instructions in
[Bring your own agent](https://github.com/livetemplate/prereview/blob/main/docs/integrations.md#bring-your-own-agent).

The skill stays current by itself: each launch refreshes it to match the binary.

## 4. Confirm the skill is there

Check that the installed file from your row exists, for example:

```sh
test -f ~/.claude/skills/prereview/SKILL.md && echo installed
```

## 5. In a sandbox, give prereview its public address

If you run in a sandbox the user reaches through a proxy, such as exe.dev or
GitHub Codespaces, the review page has to be opened at the proxy's address.
Set it once, so every launch prints that address. Replace `your-vm` and the
port with the sandbox's own:

```sh
export PREREVIEW_PUBLIC_URL='https://your-vm.exe.xyz:4799'
```

If `prereview --help` doesn't list `--public-url`, run `prereview --update`
first. See [Behind a proxy or in a sandbox](https://github.com/livetemplate/prereview/blob/main/docs/cli.md#behind-a-proxy-or-in-a-sandbox)
for other sandboxes and when to add `--host 0.0.0.0`.

## 6. Read the skill, then start

Read the installed file from your row in full. It explains how to start a
review, wait for the user's comments, apply them, and mark each one done. Then
tell the user prereview is set up, and how to start a review in your agent
(Claude Code: `/prereview`, or "review my changes"; if Claude Code says the
skill is unknown, the user runs `/reload`).
