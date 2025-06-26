# AGENTS.md - Instructions for AI Agents

This file provides guidance for AI agents working on the `gitaudit` codebase.

## Project Overview

`gitaudit` is a Go command-line tool that:
1. Takes a Git repository path and a commit ID as input.
2. Identifies all commits between `HEAD` and the specified commit (inclusive).
3. For each commit, generates a patch.
4. Sends this patch to a configurable Ollama endpoint to get a detailed commit message.
5. Writes all generated messages to `gitaudit.txt`.
6. Configuration is stored in `~/.gitaudit` (JSON format).

## Development Guidelines

### Code Style
- Follow standard Go formatting (`gofmt`).
- Write clear, commented code, especially for complex logic (e.g., Git command interactions, API calls).
- Ensure error handling is robust. Errors should be propagated or handled gracefully, providing informative messages to the user.

### Git Usage
- Git commands are executed via `os/exec`. Ensure these commands are constructed safely and their outputs/errors are handled correctly.
- Pay attention to Git version differences if using newer or less common Git features, though current usage is fairly standard.

### API Interaction (Ollama)
- The Ollama API interaction involves sending a JSON request and parsing a JSON response.
- The prompt sent to Ollama is crucial. If requirements for the generated commit message change, update the prompt string in `main.go`.
- The `callOllama` function includes a timeout. If this needs adjustment, consider if it should be configurable.

### Configuration
- The configuration file `~/.gitaudit` is critical. Ensure that any changes to configuration options are reflected in `loadConfig` and documented in `README.md`.

### Testing
- **Manual Testing:** Before submitting changes, manually test the tool with a local Git repository.
    - Test with a valid Ollama endpoint if possible.
    - If a live Ollama endpoint is not available for testing, ensure the parts of the code that *don't* depend on Ollama (Git operations, file I/O, argument parsing, etc.) are functioning correctly. The current manual test in the plan (step 8) simulates this.
    - Test edge cases:
        - Repository not found.
        - Invalid commit ID.
        - Empty commit range.
        - `~/.gitaudit` file missing or malformed.
- **Automated Tests:** While not currently implemented, future contributions could include unit tests for specific functions (e.g., parsing, non-Git utility functions) and integration tests (potentially using a mock Git environment or a mock Ollama server).

### Dependencies
- The project uses only standard Go libraries. If adding external dependencies, use Go modules (`go get`, update `go.mod`, `go.sum`).

## Workflow for Agents
1. **Understand the Task:** Clarify any ambiguities in the request.
2. **Plan:** Outline the steps to implement the changes. Use `set_plan`.
3. **Implement:** Write code, following the guidelines above.
4. **Test:** Perform manual testing as described. If you add new functionality, consider if new test cases are needed.
5. **Document:** Update `README.md` if user-facing changes are made (e.g., new CLI options, configuration changes). Update this `AGENTS.md` if there are new considerations for future AI development.
6. **Submit:** Use a clear commit message.
```
