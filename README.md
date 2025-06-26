# Git Audit

Git Audit is a command-line tool written in Go that analyzes a range of commits in a Git repository. For each commit, it uses an AI model via a configurable Ollama endpoint to generate a new, highly detailed commit message. The final output is a single text file (`gitaudit.txt`) containing all the generated messages.

## Features

- Analyzes a specified range of Git commits (from `HEAD` down to a given commit ID).
- Generates a patch for each commit.
- Sends the patch to an Ollama endpoint to generate a detailed commit message.
- Consolidates all AI-generated messages into a single `gitaudit.txt` file.
- Configurable Ollama endpoint and model via `~/.gitaudit` file.

## Prerequisites

- Go (version 1.18 or higher recommended)
- Git
- An accessible Ollama instance with a downloaded model.

## Installation

1.  **Clone the repository (or ensure you have the source code):**
    ```bash
    # If you have cloned it:
    # cd path/to/gitaudit
    ```

2.  **Build the application:**
    ```bash
    go build .
    ```
    This will create an executable named `gitaudit` in the current directory.

## Configuration

Before running the application, you need to create a configuration file in your home directory named `.gitaudit`. This file should be in JSON format and specify the Ollama endpoint and model.

**Example `~/.gitaudit`:**
```json
{
  "ollama_endpoint": "http://localhost:11434/api/generate",
  "ollama_model": "llama2"
}
```

- `ollama_endpoint`: The full URL to your Ollama API's generation endpoint.
- `ollama_model`: The name of the Ollama model you wish to use (e.g., `llama2`, `mistral`, etc.). Ensure this model is available on your Ollama instance.

## Usage

Run the `gitaudit` executable with the following flags:

```bash
./gitaudit -repo <path_to_git_repository> -commit <oldest_commit_id>
```

- `-repo <path_to_git_repository>`: (Optional) Path to the Git repository. Defaults to the current directory (`.`).
- `-commit <oldest_commit_id>`: (Required) The commit ID to audit down to. The program will process commits from `HEAD` to this specified commit, inclusive.

**Example:**

```bash
./gitaudit -repo /path/to/my/project -commit abc1234
```

This will:
1. Read commit history from `/path/to/my/project`.
2. Process all commits from the current `HEAD` down to (and including) commit `abc1234`.
3. Contact the Ollama instance defined in `~/.gitaudit`.
4. Generate detailed commit messages.
5. Write all generated messages to a file named `gitaudit.txt` in the directory where `gitaudit` was executed.
6. Print a list of any commits that failed during processing.

## Output

- **Console:** Progress messages, errors, and a summary of processed and failed commits.
- **`gitaudit.txt`:** A text file created in the current working directory. Each entry in this file corresponds to a commit in the specified range (ordered newest to oldest) and includes:
    - Git commit hash
    - Git commit author
    - Git commit date
    - The AI-generated detailed summary
    
    Entries are separated by `---`. An example entry looks like:
    ```
    Commit: <hash_value>
    Author: <author_name>
    Date: <commit_date>

    <AI-generated summary text...>
    ---
    ```

## Development

To make changes to the tool:
1. Modify the Go source files (`.go`).
2. Rebuild the application using `go build .`.
```

