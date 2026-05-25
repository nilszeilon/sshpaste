package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: sshpaste <host> [ssh options...]\n")
		fmt.Fprintf(os.Stderr, "\n  Wraps SSH with auto-upload of dragged files.\n")
		fmt.Fprintf(os.Stderr, "  Drag an image into the terminal — it lands in /tmp/file/ on the server.\n")
		fmt.Fprintf(os.Stderr, "  The local path is silently replaced with the remote path.\n")
		os.Exit(1)
	}

	host := os.Args[1]
	sshArgs := buildSSHArgs(host, os.Args[2:])
	runProxy(host, sshArgs)
}

func buildSSHArgs(host string, extra []string) []string {
	args := []string{"-tt", "-o", "StrictHostKeyChecking=no"}
	args = append(args, extra...)

	dest := host
	if !strings.Contains(dest, "@") {
		dest = "root@" + dest
	}
	args = append(args, dest)

	return args
}

func runProxy(host string, sshArgs []string) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshpaste: failed to set raw mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshpaste: ssh not found in PATH\n")
		os.Exit(1)
	}

	cmd := exec.Command(sshPath, sshArgs...)
	winSize, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		winSize = &pty.Winsize{Rows: 24, Cols: 80}
	}

	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(winSize.Rows),
		Cols: uint16(winSize.Cols),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshpaste: failed to start SSH: %v\n", err)
		os.Exit(1)
	}
	defer ptyFile.Close()

	winCh := make(chan os.Signal, 1)
	signal.Notify(winCh, syscall.SIGWINCH)
	defer signal.Stop(winCh)
	go func() {
		for range winCh {
			if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
				pty.Setsize(ptyFile, &pty.Winsize{Rows: uint16(ws.Rows), Cols: uint16(ws.Cols)})
			}
		}
	}()

	done := make(chan struct{}, 2)

	go func() {
		io.Copy(os.Stdout, ptyFile)
		done <- struct{}{}
	}()

	go func() {
		proxyStdin(ptyFile, host)
		done <- struct{}{}
	}()

	<-done
	cmd.Process.Kill()
}

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

func proxyStdin(ptyFile *os.File, host string) {
	buf := make([]byte, 32768)
	var pending []byte

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		data := append(pending, buf[:n]...)
		pending = nil

		for len(data) > 0 {
			startIdx := bytesIndex(data, pasteStart)
			if startIdx == -1 {
				ptyFile.Write(data)
				break
			}

			ptyFile.Write(data[:startIdx])
			data = data[startIdx+len(pasteStart):]

			endIdx := bytesIndex(data, pasteEnd)
			if endIdx == -1 {
				pending = append(pending, pasteStart...)
				pending = append(pending, data...)
				if len(pending) > 1<<20 {
					ptyFile.Write(pending)
					pending = nil
				}
				break
			}

			content := data[:endIdx]
			data = data[endIdx+len(pasteEnd):]

			replaced := scanAndReplace(content, host)

			ptyFile.Write(pasteStart)
			ptyFile.Write(replaced)
			ptyFile.Write(pasteEnd)
		}
	}
}

func bytesIndex(s, sep []byte) int {
	for i := 0; i <= len(s)-len(sep); i++ {
		if equal(s[i:i+len(sep)], sep) {
			return i
		}
	}
	return -1
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func shouldUpload(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".svg",
		".mp4", ".mov", ".avi", ".mkv", ".webm", ".mp3", ".wav", ".flac",
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
		".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar",
		".txt", ".md", ".log", ".csv", ".json", ".xml", ".yaml", ".yml",
		".html", ".css", ".js", ".ts", ".go", ".py", ".rs", ".java",
		".c", ".h", ".cpp", ".hpp":
		return true
	}
	return false
}

func scanAndReplace(line []byte, host string) []byte {
	s := string(line)
	var result strings.Builder
	i := 0
	for i < len(s) {
		slashIdx := strings.Index(s[i:], "/")
		if slashIdx == -1 {
			result.WriteString(s[i:])
			break
		}
		slashIdx += i
		result.WriteString(s[i:slashIdx])

		end := slashIdx + 1
		escaped := false
		for end < len(s) {
			c := s[end]
			if escaped {
				escaped = false
				end++
				continue
			}
			if c == '\\' {
				escaped = true
				end++
				continue
			}
			if c == ' ' || c == '\r' || c == '\n' || c == '\t' {
				break
			}
			end++
		}

		candidate := s[slashIdx:end]
		resolved := candidate
		if _, err := os.Stat(resolved); err != nil {
			unescaped := strings.ReplaceAll(candidate, "\\ ", " ")
			if _, err := os.Stat(unescaped); err == nil {
				resolved = unescaped
			}
		}

		if info, err := os.Stat(resolved); err == nil && !info.IsDir() && len(resolved) > 1 && shouldUpload(resolved) {
			if remotePath := uploadFile(resolved, host); remotePath != "" {
				result.WriteString(remotePath)
			} else {
				result.WriteString(candidate)
			}
		} else {
			result.WriteString(candidate)
		}
		i = end
	}
	return []byte(result.String())
}

func uploadFile(localPath, host string) string {
	filename := filepath.Base(localPath)
	safeName := strings.ReplaceAll(filename, " ", "_")
	remotePath := "/tmp/file/" + safeName

	dest := host
	if !strings.Contains(dest, "@") {
		dest = "root@" + dest
	}

	scpArgs := []string{"-o", "StrictHostKeyChecking=no", "-q"}

	exec.Command("ssh", append(scpArgs, dest, "mkdir -p /tmp/file")...).Run()
	cmd := exec.Command("scp", append(scpArgs, localPath, dest+":"+remotePath)...)
	if err := cmd.Run(); err != nil {
		return ""
	}
	return remotePath
}
