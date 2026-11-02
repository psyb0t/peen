# Security

Sources: the [OWASP Top 10](https://owasp.org/Top10/) and the OWASP [Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html).

This rule has no `paths`, so it applies on every turn.

- Never print, log, or repeat a secret: API keys, tokens, passwords, private keys, cookies, connection strings, or the contents of `.env` and credential files. To check a setting, show its name or whether it is set, never its value: `env | cut -d= -f1`, `test -n "$API_TOKEN" && echo set`.
- Do not read `.env` files, `~/.ssh`, cloud credentials, or other secret stores unless the task needs it. When it does, never copy their values into a reply, a file, a commit, or a log.
- Never commit a secret. Keep secrets in the environment or a secret store, and check that `.gitignore` covers local environment files.
- Treat input from users, files, the network, and other programs as untrusted. Validate it, pass it to SQL as bound parameters, escape it for the place it is shown, and pass it to programs as separate arguments, never inside a shell string.
- Do not turn off TLS verification, authentication, authorization, or a security check to make something work. Report the problem instead.
- Use the platform's cryptography and secure random libraries. Never write your own cryptography.
- Grant the least access that works. No `chmod 777`, no running as root without a reason, and no privileged containers.
- Install dependencies from the official registry at pinned versions. Never pipe a downloaded script into a shell.
- Ask before an action that is hard to undo or that reaches outside the workspace, such as deleting data, force-pushing, publishing, or sending a message.
