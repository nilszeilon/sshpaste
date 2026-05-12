
# sshpaste

SSH wrapper that auto-uploads dragged files.

```bash
sshpaste myserver
```

Drag an image into the terminal — it lands in `/tmp/file/` on the server.
The local path is silently replaced with the remote path.

## Install

```bash
go install github.com/nilszeilon/sshpaste@latest
```

## How it works

Wraps SSH in a PTY proxy. When you drag a file into the terminal, the terminal
wraps it in bracketed paste markers (`\e[200~` ... `\e[201~`). sshpaste detects
this, SCPs the file to `/tmp/file/`, and replaces the path before
forwarding to the server.

Normal typing, escape sequences, and control characters pass through instantly
— only paste content is buffered.

## SSH keys

Uses your default SSH key. Pass extra SSH options after the host:

```bash
sshpaste myserver -p 2222 -i ~/.ssh/mykey
```
