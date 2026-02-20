# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| latest  | Yes       |

## Reporting a vulnerability

If you discover a security vulnerability, please report it responsibly.

**Do not open a public issue.**

Instead, please email security concerns to the maintainers or use [GitHub's private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability).

### What to include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response timeline

- **Acknowledgment**: within 48 hours
- **Assessment**: within 1 week
- **Fix**: depends on severity, typically within 2 weeks for critical issues

## Security considerations

### API keys

- Never commit API keys (Jina AI, Ollama) to the repository
- Use environment variables or `.env` files (gitignored by default)
- The `.env.example` file contains only placeholder values

### File access

- The server uses a **PathGuard** that restricts file access to explicitly allowlisted paths
- Path traversal attacks are prevented by resolving and validating all paths
- Only paths under `MCP_ROOT` and `MCP_ALLOW_DIRS` are accessible

### Data storage

- Memory and vector index data are stored in local SQLite databases
- No data is sent to external services except embedding API calls (Jina AI or Ollama)
- Embedding API calls contain only document text chunks, no metadata or credentials
