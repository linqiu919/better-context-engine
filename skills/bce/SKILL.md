---
name: bce
description: Codebase retrieval with Better Context Engine (BCE). Searches the current project with hybrid lexical + structural + semantic retrieval and returns the relevant files, line ranges and snippets. Use when you need to locate where something lives, trace a call chain, or understand unfamiliar code before editing — prefer it over grep for natural-language, cross-file or cross-language questions. Also rewrites a rough prompt into a precise one grounded in the codebase.
compatibility: Requires Node.js (npx) and network access to a BCE service. Environment variables BCE_BASE_URL and BCE_TOKEN must be set. The project must be a Git repository.
metadata:
  author: better-context-engine
  client: bce-tool (npm)
---

# BCE codebase retrieval

BCE is a hosted context engine. The `bce-tool` CLI indexes the working
directory incrementally (content-addressed, unchanged files are skipped), asks
the service, and prints the retrieval to stdout. Logs go to stderr, so stdout
is the answer and nothing else.

## One-time setup

The user sets two environment variables in their shell profile:

```bash
export BCE_BASE_URL="https://bce.wxnext.top"   # your BCE service
export BCE_TOKEN="bce_..."                      # personal token: console → Account → BCE token
```

PowerShell: `$env:BCE_BASE_URL` / `$env:BCE_TOKEN`.

If either is missing, stop and ask the user for them; do not guess a token.

## Search

Always run from the project root (the command indexes the current directory):

```bash
cd <project-root> && npx -y bce-tool --base-url "$BCE_BASE_URL" --token "$BCE_TOKEN" --search "<question>"
```

- The first run uploads the whole project; expect it to take a while on large
  repos. Later runs only upload changed files.
- Output shape:

  ```xml
  Note: ...                                   <!-- optional, see below -->
  <codebase_context>
    <file path="internal/auth/login.go" start_line="40" end_line="88"><![CDATA[...]]></file>
    ...
  </codebase_context>
  <related_symbols hint="...">               <!-- optional grep leads -->
    <symbol name="VerifyPassword" kind="func" path="internal/auth/password.go"/>
  </related_symbols>
  ```

  Read the `<file>` hits, then open the cited paths at the cited lines with
  your own tools. `<related_symbols>` are definitions referenced by the hits
  but not shown — grep for them if you need to go deeper.
- Leading `Note:` lines change how to read the result:
  - "no strongly matching code was found" — low confidence; rephrase, or the
    feature may live in another repository. Do not treat the fragments as
    the implementation.
  - "semantic index did not participate" — keyword-only results this time;
    retry later for concept-level questions.
  - "exploratory query" — broad mode; excerpts are trimmed skeletons, read
    the cited ranges for full detail.
  - "never uploaded to the server" — the local cache is out of sync: delete
    `.ace-tool/index.bin` in the project root and run again.

### When to use

- Exploratory questions: "where is X handled", "how does Y flow work", "what
  calls Z", "which config controls W".
- Anything spanning several files or layers (router → service → model).
- Questions in a language other than the code's (Chinese, Japanese, ...) —
  the engine expands the terms.
- Before editing code you have not read yet.

Do not use it for exact-string lookups you can grep, or for reading a file you
already know the path of.

### Writing good queries

- Describe the behaviour, not the identifier: "where are login failures rate
  limited" beats "loginFailCount".
- One intent per query; run several searches rather than one compound one.
- Include the framework or layer if it disambiguates ("Spring controller for
  order refunds").
- Keep it under ~30 words.

## Enhance a prompt

Turn a rough request into a prompt that already names the right files and
lines (uses the latest indexed state of this project):

```bash
cd <project-root> && npx -y bce-tool --base-url "$BCE_BASE_URL" --token "$BCE_TOKEN" --enhance-prompt "<rough request>"
```

Use the printed text as the task statement before you start working.

## Notes

- Non-Git directories are refused by default (guards against indexing
  Downloads-style folders). Add `--allow-non-git` only if the user confirms.
- Projects over 150 MB of indexable source are refused.
- `.gitignore`, `.bceignore` and common build/vendor directories are excluded
  automatically.
- The same `.ace-tool/index.bin` cache is shared with the bce-tool MCP server,
  so mixing the Skill and MCP in one project is fine.
