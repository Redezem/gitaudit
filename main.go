package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var interrupted bool = false
var mu sync.Mutex // To protect access to `interrupted` if needed, though current usage is simple

// OllamaRequest defines the structure for requests to the Ollama API.
type OllamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"` // Set to false for a single response
}

// OllamaResponse defines the structure for responses from the Ollama API.
// We are interested in the "response" field for non-streaming.
type OllamaResponse struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Response  string    `json:"response"`
	Done      bool      `json:"done"`
	// Other fields might be present depending on the response, like context, total_duration, etc.
}

// CommitAuditData holds the Git metadata and the generated summary for a commit.
type CommitAuditData struct {
	Hash    string
	Author  string
	Date    string
	Summary string
}

func main() {
	repoPath := flag.String("repo", ".", "Path to the Git repository")
	commitID := flag.String("commit", "", "The oldest commit ID to audit to")

	flag.Parse()

	if *commitID == "" {
		fmt.Println("Error: commit ID is required.")
		flag.Usage()
		os.Exit(1)
	}

	fmt.Printf("Repository Path: %s\n", *repoPath)
	fmt.Printf("Commit ID: %s\n", *commitID)

	config, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Ollama Endpoint: %s\n", config.OllamaEndpoint)
	fmt.Printf("Ollama Model: %s\n", config.OllamaModel)

	// Setup signal handling for Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nCtrl+C received. Shutting down gracefully...")
		mu.Lock()
		interrupted = true
		mu.Unlock()
	}()

	commitHashes, err := getCommitHashes(*repoPath, *commitID)
	if err != nil {
		fmt.Printf("Error getting commit hashes: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Commit hashes to process:")
	for _, hash := range commitHashes {
		fmt.Println(hash)
	}

	var allAuditedCommits []CommitAuditData // Slice to store all successfully audited commits
	var retryQueueCommits []string          // Slice to store commit hashes that need retrying

	// Initial processing loop
	fmt.Println("--- Initial Processing Pass ---")
	for _, commitHash := range commitHashes {
		mu.Lock()
		if interrupted {
			mu.Unlock()
			fmt.Println("Interrupted during initial processing pass.")
			// Add remaining initial commits to retryQueue so they are reported as pending
			// Find current commitHash in commitHashes and add the rest
			for i, h := range commitHashes {
				if h == commitHash {
					retryQueueCommits = append(retryQueueCommits, commitHashes[i:]...)
					break
				}
			}
			break // Exit initial processing loop
		}
		mu.Unlock()

		fmt.Printf("Processing commit: %s\n", commitHash)
		patch, err := getPatchForCommit(*repoPath, commitHash)
		if err != nil {
			errMsg := fmt.Sprintf("Error generating patch for commit %s: %v. Adding to retry queue.", commitHash, err)
			fmt.Println(errMsg)
			retryQueueCommits = append(retryQueueCommits, commitHash)
			continue
		}

		prompt := fmt.Sprintf(`Given the following Git patch, please generate a highly detailed and descriptive Git commit message. The message should cover:
1. A summary of the changes.
2. The reasoning behind the changes (why they were made).
3. Any problems that were encountered (if apparent from the patch or commit message).
4. The intended purpose or goal of the commit.

Do not include the "Patch:" prefix or any introductory phrases like "Here's a commit message:". Output only the commit message itself.

Patch:
%s`, patch)

		generatedMessage, err := callOllama(config.OllamaEndpoint, config.OllamaModel, prompt)
		if err != nil {
			errMsg := fmt.Sprintf("Error calling Ollama for commit %s: %v. Adding to retry queue.", commitHash, err)
			fmt.Println(errMsg)
			retryQueueCommits = append(retryQueueCommits, commitHash)
			continue
		}

		commitGitHash, author, date, err := getCommitMetadata(*repoPath, commitHash)
		if err != nil {
			errMsg := fmt.Sprintf("Error getting metadata for commit %s: %v. Adding to retry queue.", commitHash, err)
			fmt.Println(errMsg)
			retryQueueCommits = append(retryQueueCommits, commitHash)
			continue
		}

		fmt.Printf("Successfully processed commit %s (Got Ollama summary and Git metadata)\n", commitHash)
		auditData := CommitAuditData{
			Hash:    commitGitHash,
			Author:  author,
			Date:    date,
			Summary: generatedMessage,
		}
		allAuditedCommits = append(allAuditedCommits, auditData)
	}

	// Retry loop
	if len(retryQueueCommits) > 0 && !interrupted { // Check interrupted flag before starting retry loop
		fmt.Println("\n--- Starting Retry Processing ---")
	}
	for len(retryQueueCommits) > 0 {
		mu.Lock()
		if interrupted {
			mu.Unlock()
			fmt.Println("Interrupted during retry processing.")
			break // Exit retry loop
		}
		mu.Unlock()

		fmt.Printf("Commits in retry queue: %d\n", len(retryQueueCommits))
		currentFailures := 0 // To detect if all attempts in a retry pass fail

		var nextRetryQueue []string
		for _, commitHash := range retryQueueCommits {
			mu.Lock()
			if interrupted {
				mu.Unlock()
				// Add current and remaining retry commits to nextRetryQueue to be reported as pending
				// Find current commitHash in retryQueueCommits and add it and the rest
				for i, h := range retryQueueCommits {
					if h == commitHash {
						nextRetryQueue = append(nextRetryQueue, retryQueueCommits[i:]...)
						break
					}
				}
				break // Exit inner loop for this pass
			}
			mu.Unlock()

			fmt.Printf("Retrying commit: %s\n", commitHash)
			patch, err := getPatchForCommit(*repoPath, commitHash)
			if err != nil {
				errMsg := fmt.Sprintf("Error generating patch for commit %s during retry: %v. Will retry again.", commitHash, err)
				fmt.Println(errMsg)
				nextRetryQueue = append(nextRetryQueue, commitHash)
				currentFailures++
				continue
			}

			prompt := fmt.Sprintf(`Given the following Git patch, please generate a highly detailed and descriptive Git commit message. The message should cover:
1. A summary of the changes.
2. The reasoning behind the changes (why they were made).
3. Any problems that were encountered (if apparent from the patch or commit message).
4. The intended purpose or goal of the commit.

Do not include the "Patch:" prefix or any introductory phrases like "Here's a commit message:". Output only the commit message itself.

Patch:
%s`, patch)

			generatedMessage, err := callOllama(config.OllamaEndpoint, config.OllamaModel, prompt)
			if err != nil {
				errMsg := fmt.Sprintf("Error calling Ollama for commit %s during retry: %v. Will retry again.", commitHash, err)
				fmt.Println(errMsg)
				nextRetryQueue = append(nextRetryQueue, commitHash)
				currentFailures++
				continue
			}

			commitGitHash, author, date, err := getCommitMetadata(*repoPath, commitHash)
			if err != nil {
				errMsg := fmt.Sprintf("Error getting metadata for commit %s during retry: %v. Will retry again.", commitHash, err)
				fmt.Println(errMsg)
				nextRetryQueue = append(nextRetryQueue, commitHash)
				currentFailures++
				continue
			}
			fmt.Printf("Successfully processed commit %s on retry (Got Ollama summary and Git metadata)\n", commitHash)
			auditData := CommitAuditData{
				Hash:    commitGitHash,
				Author:  author,
				Date:    date,
				Summary: generatedMessage,
			}
			allAuditedCommits = append(allAuditedCommits, auditData) // Add to the main list
		}
		retryQueueCommits = nextRetryQueue

		if len(retryQueueCommits) > 0 && currentFailures == len(retryQueueCommits) && !interrupted {
			fmt.Printf("All %d commits in the current retry pass failed. Retrying them again in the next pass.\n", currentFailures)
			// No sleep here as per "ad infinitum" but in a real-world scenario, a small delay might be added.
		}
		// The duplicated retryQueueCommits = nextRetryQueue and the subsequent if block were simplified
		// as the state of interrupted is checked at the beginning of the outer loop and inner loop.
	}

	// Write all successful audit data to gitaudit.txt
	if len(allAuditedCommits) > 0 {
		outputFileName := "gitaudit.txt"
		err = writeMessagesToFile(outputFileName, allAuditedCommits) // Pass allAuditedCommits
		if err != nil {
			fmt.Printf("Error writing audited commit data to file %s: %v\n", outputFileName, err)
		} else {
			fmt.Printf("\nSuccessfully wrote %d audited commit entries to %s\n", len(allAuditedCommits), outputFileName)
		}
	} else {
		fmt.Println("\nNo audited commit data was successfully generated to write to file.")
	}

	mu.Lock()
	isInterrupted := interrupted
	mu.Unlock()

	if isInterrupted {
		fmt.Println("\nProcess was interrupted.")
		if len(retryQueueCommits) > 0 {
			fmt.Printf("The following %d commits were pending processing or retry:\n", len(retryQueueCommits))
			// Remove duplicates that might have occurred if interruption happened during list copying
			uniquePendingCommits := make(map[string]bool)
			var finalList []string
			for _, commitHash := range retryQueueCommits {
				if !uniquePendingCommits[commitHash] {
					uniquePendingCommits[commitHash] = true
					finalList = append(finalList, commitHash)
				}
			}
			for _, commitHash := range finalList {
				fmt.Println(commitHash)
			}
		} else {
			fmt.Println("No commits were pending retry.")
		}
	} else {
		fmt.Println("\nAll commits processed successfully.")
	}
}

// writeMessagesToFile writes a list of CommitAuditData to the specified file,
// with each entry formatted and separated by a standard delimiter.
func writeMessagesToFile(filename string, auditedCommits []CommitAuditData) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer file.Close()

	for i, data := range auditedCommits {
		entry := fmt.Sprintf("Commit: %s\nAuthor: %s\nDate: %s\n\n%s\n",
			data.Hash, data.Author, data.Date, data.Summary)
		_, err := file.WriteString(entry)
		if err != nil {
			return fmt.Errorf("failed to write audit data to file for commit %s: %w", data.Hash, err)
		}

		// Add a separator between entries, but not after the last one.
		if i < len(auditedCommits)-1 {
			_, err = file.WriteString("\n---\n\n") // Adjusted separator for better readability between entries
			if err != nil {
				return fmt.Errorf("failed to write separator to file after commit %s: %w", data.Hash, err)
			}
		}
	}
	return nil
}

// callOllama sends a prompt to the Ollama API and returns the generated message.
func callOllama(endpoint, model, promptStr string) (string, error) {
	ollamaReq := OllamaRequest{
		Model:  model,
		Prompt: promptStr,
		Stream: false, // We want a single consolidated response
	}

	reqBodyBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Ollama request: %w", err)
	}

	httpClient := &http.Client{Timeout: 60 * time.Second} // Configurable timeout
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request to Ollama: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to send request to Ollama endpoint %s: %w", endpoint, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		// Try to read body for more error info
		var bodyBytes []byte
		bodyBytes, _ = io.ReadAll(httpResp.Body) // Ignore error on read, primary error is status code
		return "", fmt.Errorf("Ollama API request failed with status %s: %s", httpResp.Status, string(bodyBytes))
	}

	var ollamaResp OllamaResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&ollamaResp); err != nil {
		return "", fmt.Errorf("failed to decode Ollama response: %w", err)
	}

	if !ollamaResp.Done {
		// This might happen if stream=false is not fully respected or if there's an issue.
		// Or, if the model sends intermediate messages even with stream=false.
		// For a simple non-streaming request, `done` should ideally be true on the single response.
		// However, the key is the `response` field.
		fmt.Println("Warning: Ollama response indicates 'done' is false for a non-streaming request.")
	}

	return strings.TrimSpace(ollamaResp.Response), nil
}

// getPatchForCommit generates a patch for a given commit hash.
// The patch includes the original commit message and the full diff.
func getPatchForCommit(repoPath, commitHash string) (string, error) {
	// `git show --patch <commitHash>` or `git format-patch -1 --stdout <commitHash>`
	// `git show` is simpler as it includes the commit message and diff directly.
	// `git format-patch` is more for creating patch files to be applied with `git am`.
	// For sending to an LLM, `git show --patch` which includes commit metadata is good.
	// The problem asks for "original commit message and the full set of changes (diff)".
	// `git show --patch <commitHash>` does exactly this.
	// `git show --patch --pretty=fuller <commitHash>` might give more detailed metadata if needed.
	// For now, default `git show --patch` is fine.

	cmd := exec.Command("git", "-C", repoPath, "show", "--patch", commitHash)
	patchBytes, err := cmd.Output()
	if err != nil {
		// Attempt to get stderr for more context
		errMsg := fmt.Sprintf("failed to execute git show for commit %s: %v", commitHash, err)
		if ee, ok := err.(*exec.ExitError); ok {
			errMsg = fmt.Sprintf("%s. Stderr: %s", errMsg, string(ee.Stderr))
		}
		return "", fmt.Errorf(errMsg)
	}
	return string(patchBytes), nil
}

// getCommitMetadata retrieves the hash, author, and date for a given commit.
func getCommitMetadata(repoPath, commitHash string) (hash, author, date string, err error) {
	cmd := exec.Command("git", "-C", repoPath, "show", "-s", fmt.Sprintf("--format=%s", "%H%n%an%n%ai"), commitHash)
	output, err := cmd.Output()
	if err != nil {
		errMsg := fmt.Sprintf("failed to execute git show for metadata on commit %s: %v", commitHash, err)
		if ee, ok := err.(*exec.ExitError); ok {
			errMsg = fmt.Sprintf("%s. Stderr: %s", errMsg, string(ee.Stderr))
		}
		return "", "", "", fmt.Errorf(errMsg)
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("unexpected format from git show for metadata on commit %s: expected 3 lines, got %d. Output: %s", commitHash, len(parts), string(output))
	}

	return parts[0], parts[1], parts[2], nil
}

// getCommitHashes returns a list of commit hashes from HEAD to the specified endCommitID (inclusive)
// in chronological order (newest to oldest).
func getCommitHashes(repoPath, endCommitID string) ([]string, error) {
	// git log --pretty=format:%H HEAD...endCommitID
	// We need to include the endCommitID itself.
	// The range HEAD..endCommitID (two dots) includes commits reachable from HEAD but not from endCommitID.
	// The range HEAD...endCommitID (three dots) includes commits reachable from either HEAD or endCommitID but not both.
	// Neither is quite right for "all commits between HEAD and endCommitID, inclusive".

	// Let's list commits from HEAD down to the target commit.
	// `git log --pretty=format:%H <endCommitID>^..HEAD` should work.
	// The ^ after endCommitID makes it exclusive of endCommitID's parent, so endCommitID itself is included
	// The order will be newest (HEAD) to oldest (endCommitID).

	// Validate that repoPath is a git repository.
	// Using `git rev-parse --is-inside-work-tree` is a more robust way to check.
	cmdCheckRepo := exec.Command("git", "-C", repoPath, "rev-parse", "--is-inside-work-tree")
	if err := cmdCheckRepo.Run(); err != nil {
		// This command outputs "true" or "false" to stdout and exits 0 if it's a repo (even if not top-level).
		// It exits non-zero if not a git repo path.
		return nil, fmt.Errorf("path %s is not a git repository or git command failed: %w", repoPath, err)
	}

	// Ensure endCommitID is a full SHA and exists in the repo.
	// `git rev-parse --verify <commitID>` will error if commit doesn't exist.
	cmdResolveEndCommit := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", endCommitID)
	resolvedEndCommitBytes, err := cmdResolveEndCommit.Output()
	if err != nil {
		// Error from git rev-parse includes the commit ID, so the message is informative.
		return nil, fmt.Errorf("failed to resolve commit ID %s in repository %s: %w", endCommitID, repoPath, err)
	}
	resolvedEndCommitID := strings.TrimSpace(string(resolvedEndCommitBytes))

	// Get all commit hashes from HEAD, newest first.
	// `git rev-list HEAD` lists commit objects in reverse chronological order.
	cmd := exec.Command("git", "-C", repoPath, "rev-list", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to execute git rev-list HEAD: %w\nOutput: %s", err, string(output))
	}

	allCommits := strings.Split(strings.TrimSpace(string(output)), "\n")
	var resultCommits []string
	foundEndCommit := false

	for _, commitHash := range allCommits {
		if commitHash == "" { // Handle potential empty lines if any
			continue
		}
		resultCommits = append(resultCommits, commitHash)
		if commitHash == resolvedEndCommitID {
			foundEndCommit = true
			break
		}
	}

	if !foundEndCommit {
		return nil, fmt.Errorf("commit ID %s not found in the history of HEAD or is not an ancestor", endCommitID)
	}

	return resultCommits, nil
}

// Config holds the configuration settings for Git Audit
type Config struct {
	OllamaEndpoint string `json:"ollama_endpoint"`
	OllamaModel    string `json:"ollama_model"`
}

// loadConfig reads the configuration from ~/.gitaudit
func loadConfig() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	configPath := fmt.Sprintf("%s/.gitaudit", homeDir)
	configFile, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s. Please create it with 'ollama_endpoint' and 'ollama_model'", configPath)
		}
		return nil, fmt.Errorf("failed to open config file %s: %w", configPath, err)
	}
	defer configFile.Close()

	var config Config
	// For simplicity, we'll use a simple key=value format for now.
	// A more robust solution would use JSON, YAML, or TOML.
	// Example .gitaudit file:
	// ollama_endpoint=http://localhost:11434/api/generate
	// ollama_model=llama2
	// This will be improved to use JSON parsing.

	// Read the file line by line for now
	// This will be replaced by proper JSON decoding.
	// For now, let's assume fixed values for demonstration until JSON parsing is added.
	// This is a placeholder.
	// TODO: Implement proper JSON parsing for the config file.

	// Temporary placeholder for config loading
	// We will replace this with actual file parsing logic.
	// For now, we'll hardcode to allow progress, then implement JSON.

	// Let's create a dummy .gitaudit file for testing in the current dir
	// and then implement the actual JSON parsing.

	// This will be replaced by proper JSON decoding from ~/.gitaudit
	// For now, this is a placeholder to allow other parts to be built.
	// Actual implementation will use json.Decoder

	// Switching to use json.Decoder as planned.
	// Need to import "encoding/json"
	// The config file should be in JSON format, e.g.:
	// {
	//   "ollama_endpoint": "http://localhost:11434/api/generate",
	//   "ollama_model": "llama2"
	// }

	// Corrected approach: Use encoding/json
	// Need to add `import "encoding/json"`
	// The struct tags `json:"..."` are already in place for this.

	// The file reading part is correct, now decode it.
	// Need to add "encoding/json" to imports.

	// The file opening logic is fine. Now decode.
	decoder := json.NewDecoder(configFile)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode config file %s: %w. Ensure it is valid JSON", configPath, err)
	}

	if config.OllamaEndpoint == "" || config.OllamaModel == "" {
		return nil, fmt.Errorf("config file %s must contain 'ollama_endpoint' and 'ollama_model'", configPath)
	}

	return &config, nil
}

