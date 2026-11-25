package main

/*
Image paste and file drop for persistent terminals.

An agent in a terminal runs on the remote host and cannot see the browser's clipboard or disk, so
POST /api/terminals/{id}/images and /files copy an image or file to that host's /tmp and return
the path. The browser then pastes the path into the session.

The upload is streamed straight through: nothing is stored on this server. The copy runs over
a new SSH connection from cguard using plain exec commands, so hosts without the SFTP
subsystem work and cg_sessiond is not involved.
*/

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	terminalImageMaxBytes         = 20 << 20
	terminalFileMaxBytes          = 100 << 20
	terminalFileNameMaxLength     = 100
	terminalFileExtMaxLength      = 20
	terminalUploadTimeout         = 15 * time.Minute
	terminalImageRetentionDefault = 7 * 24 * time.Hour
	terminalImageRetentionMaxDays = 365
)

// Only formats Claude can read. Decided from the image bytes, never the filename or header.
var terminalImageExtensions = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// mktemp's answer ends up unquoted inside later commands, so it must match exactly.
var terminalImageTempPath = regexp.MustCompile(`^/tmp/cg-paste-[A-Za-z0-9]+$`)

// The suffix also ends up unquoted inside later commands, and the final path is typed into the
// session, so it must contain nothing a shell treats specially.
var terminalUploadSuffix = regexp.MustCompile(`^(\.[a-z]+|-[A-Za-z0-9._-]+)$`)

var terminalFileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// terminalSafeFileName keeps the name and extension that agents and tools rely on, reduced to
// characters that need no quoting. The name comes from the browser, so it is not trusted.
func terminalSafeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	safe := terminalFileNameUnsafe.ReplaceAllString(name, "_")
	if strings.Trim(safe, "._-") == "" {
		return "file"
	}
	if len(safe) > terminalFileNameMaxLength {
		ext := filepath.Ext(safe)
		if len(ext) > terminalFileExtMaxLength {
			ext = ""
		}
		safe = safe[:terminalFileNameMaxLength-len(ext)] + ext
	}
	return safe
}

// imageBodyReader records whether the whole request body was read. Once the client stops
// sending, "cat" on the host sees a normal end of input, so its exit status says nothing about
// a cut-off image. It also tells an over-size body from a host failure. The SSH library reads
// it from its own goroutine.
type imageBodyReader struct {
	r io.Reader

	mu       sync.Mutex
	size     int64
	complete bool
	err      error
}

func (b *imageBodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.size += int64(n)
	if err == io.EOF {
		b.complete = true
	} else if err != nil {
		b.err = err
	}
	return n, err
}

func (b *imageBodyReader) result() (int64, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size, b.complete, b.err
}

// runTerminalImageCommand wraps the script in sh -c because the login shell may be fish or csh.
func runTerminalImageCommand(client *ssh.Client, script string, stdin io.Reader) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to open SSH session: %w", err)
	}
	defer session.Close()
	stdout, stderr := NewLimitedBuffer(4096), NewLimitedBuffer(4096)
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	if err := session.Run("sh -c '" + script + "'"); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	return stdout.String(), nil
}

// copyImageToHost writes the upload to a private file in the host's /tmp and returns its path
// and size. The suffix follows the random part of the name. The final name appears only after
// the whole upload arrived. Removing old files afterwards is best effort and never fails the
// upload.
func copyImageToHost(client *ssh.Client, body *imageBodyReader, suffix string, retention time.Duration) (string, int64, error) {
	if !terminalUploadSuffix.MatchString(suffix) {
		return "", 0, fmt.Errorf("unsafe file name suffix %q", suffix)
	}
	out, err := runTerminalImageCommand(client, `umask 077; mktemp /tmp/cg-paste-XXXXXXXXXXXX`, nil)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create a temporary file on the host: %w", err)
	}
	// Login scripts may print extra lines before the command's own output.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	tempPath := strings.TrimSpace(lines[len(lines)-1])
	if !terminalImageTempPath.MatchString(tempPath) {
		return "", 0, fmt.Errorf("unexpected answer from mktemp on the host: %q", tempPath)
	}

	_, writeErr := runTerminalImageCommand(client, `cat > `+tempPath, body)
	size, complete, bodyErr := body.result()
	if bodyErr != nil || !complete || writeErr != nil {
		// If this fails too, the retention sweep removes the file later.
		_, _ = runTerminalImageCommand(client, `rm -f `+tempPath, nil)
		if bodyErr != nil {
			return "", 0, bodyErr
		}
		if writeErr != nil {
			return "", 0, fmt.Errorf("failed to write the file on the host: %w", writeErr)
		}
		return "", 0, errors.New("failed to write the file on the host: the upload stopped early")
	}

	finalPath := tempPath + suffix
	if _, err := runTerminalImageCommand(client, `mv `+tempPath+` `+finalPath, nil); err != nil {
		_, _ = runTerminalImageCommand(client, `rm -f `+tempPath, nil)
		return "", 0, fmt.Errorf("failed to finish the file on the host: %w", err)
	}

	// The slash follows /tmp when it is a symlink, as on macOS.
	sweep := fmt.Sprintf(`find /tmp/ -maxdepth 1 -type f -name "cg-paste-*" -user "$(id -u)" -mmin +%d -delete`, int(retention.Minutes()))
	_, _ = runTerminalImageCommand(client, sweep, nil)
	return finalPath, size, nil
}

// prepareTerminalUpload runs the checks shared by image and file uploads and extends the
// connection's deadlines. It has answered the request when ok is false.
func (t *TerminalsAPI) prepareTerminalUpload(w http.ResponseWriter, r *http.Request, claims *Claims, settings terminalSettings, id string) (session *TerminalSession, host Host, ok bool) {
	if !settings.imagesEnabled {
		sendJSONError(w, http.StatusForbidden, "Copying images and files into terminals is disabled in configuration (terminals.images_enabled)")
		return nil, host, false
	}
	session, err := t.findSession(id)
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return nil, host, false
	}
	if session == nil || !t.canAccessSession(claims, *session) {
		sendJSONError(w, http.StatusNotFound, "Terminal session not found")
		return nil, host, false
	}
	if session.Status != "running" {
		sendJSONError(w, http.StatusConflict, "This terminal session has ended")
		return nil, host, false
	}
	host, hostErr := t.resolveTerminalHost(claims, session.HostRef)
	if hostErr != nil {
		sendJSONError(w, hostErr.status, hostErr.message)
		return nil, host, false
	}
	// A session keeps its original connection, so after a Host edit the file could land on
	// another machine, or under a login that the session's agent cannot read it as.
	if host.Host != session.Host || host.Username != session.Username {
		sendJSONError(w, http.StatusConflict, fmt.Sprintf("Host %q has changed since this session started (session: %s@%s, now: %s@%s). Start a new session to copy files.", session.HostRef, session.Username, session.Host, host.Username, host.Host))
		return nil, host, false
	}

	// The server's 30 s ReadTimeout would cut off a large upload on a slow connection.
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(terminalUploadTimeout)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline.Add(30 * time.Second))
	return session, host, true
}

// terminalUpload is what differs between an image and a file once the body has been checked.
type terminalUpload struct {
	id       string
	session  *TerminalSession
	host     Host
	reader   io.Reader
	suffix   string
	action   string // Audit action
	noun     string // "image" or "file", for messages
	tooLarge string
	details  map[string]interface{} // Extra audit details
}

// runTerminalUpload connects to the host, copies the body there and answers the request.
func (t *TerminalsAPI) runTerminalUpload(w http.ResponseWriter, r *http.Request, claims *Claims, settings terminalSettings, up terminalUpload) {
	audit := func(outcome string, details map[string]interface{}) {
		details["host_ref"] = up.session.HostRef
		details["session_id"] = up.id
		for key, value := range up.details {
			details[key] = value
		}
		writeSecurityAudit(r, claims.Username, claims.Role, up.action, up.session.HostRef, outcome, details)
	}

	// Connect handles auth and the bastion, and fails loudly rather than bypass one.
	executor := &SSHExecutor{host: up.host}
	if err := executor.Connect(); err != nil {
		audit("failed", map[string]interface{}{"error": err.Error()})
		sendJSONError(w, http.StatusBadGateway, fmt.Sprintf("Could not connect to %s to copy the %s: %v", up.session.HostRef, up.noun, err))
		return
	}
	defer executor.Close()
	timer := time.AfterFunc(terminalUploadTimeout, func() { executor.Close() })
	defer timer.Stop()

	path, size, err := copyImageToHost(executor.client, &imageBodyReader{r: up.reader}, up.suffix, settings.imageRetention)
	if err != nil {
		audit("failed", map[string]interface{}{"error": err.Error()})
		var maxBytes *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytes):
			sendJSONError(w, http.StatusRequestEntityTooLarge, up.tooLarge)
		default:
			sendJSONError(w, http.StatusBadGateway, fmt.Sprintf("Could not copy the %s to %s: %v", up.noun, up.session.HostRef, err))
		}
		return
	}
	audit("success", map[string]interface{}{"path": path, "size": size})
	sendJSONSuccess(w, http.StatusCreated, map[string]interface{}{
		"path": path, "host_ref": up.session.HostRef, "host": up.session.Host, "size": size,
	}, strings.ToUpper(up.noun[:1])+up.noun[1:]+" copied to host")
}

// handleImageUpload serves POST /api/terminals/{id}/images. The raw image is the request body.
func (t *TerminalsAPI) handleImageUpload(w http.ResponseWriter, r *http.Request, claims *Claims, settings terminalSettings, id string) {
	session, host, ok := t.prepareTerminalUpload(w, r, claims, settings, id)
	if !ok {
		return
	}
	tooLarge := fmt.Sprintf("Image is larger than the %d MB limit", terminalImageMaxBytes>>20)
	if r.ContentLength > terminalImageMaxBytes {
		sendJSONError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	reader := bufio.NewReader(http.MaxBytesReader(w, r.Body, terminalImageMaxBytes))
	head, err := reader.Peek(512)
	if err != nil && err != io.EOF {
		sendJSONError(w, http.StatusBadRequest, "Failed to read the image")
		return
	}
	if len(head) == 0 {
		sendJSONError(w, http.StatusBadRequest, "The image is empty")
		return
	}
	ext, ok := terminalImageExtensions[http.DetectContentType(head)]
	if !ok {
		sendJSONError(w, http.StatusUnsupportedMediaType, "Only PNG, JPEG, GIF and WebP images can be pasted")
		return
	}
	t.runTerminalUpload(w, r, claims, settings, terminalUpload{
		id: id, session: session, host: host, reader: reader, suffix: "." + ext,
		action: "terminal_image_upload", noun: "image", tooLarge: tooLarge, details: map[string]interface{}{},
	})
}

// handleFileUpload serves POST /api/terminals/{id}/files?name=<original name>. The raw file is
// the request body. Any type is accepted: whoever can open this session can already create any
// file on the host.
func (t *TerminalsAPI) handleFileUpload(w http.ResponseWriter, r *http.Request, claims *Claims, settings terminalSettings, id string) {
	session, host, ok := t.prepareTerminalUpload(w, r, claims, settings, id)
	if !ok {
		return
	}
	tooLarge := fmt.Sprintf("File is larger than the %d MB limit", terminalFileMaxBytes>>20)
	if r.ContentLength > terminalFileMaxBytes {
		sendJSONError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	reader := bufio.NewReader(http.MaxBytesReader(w, r.Body, terminalFileMaxBytes))
	if _, err := reader.Peek(1); err == io.EOF {
		sendJSONError(w, http.StatusBadRequest, "The file is empty")
		return
	} else if err != nil {
		sendJSONError(w, http.StatusBadRequest, "Failed to read the file")
		return
	}
	name := terminalSafeFileName(r.URL.Query().Get("name"))
	t.runTerminalUpload(w, r, claims, settings, terminalUpload{
		id: id, session: session, host: host, reader: reader, suffix: "-" + name,
		action: "terminal_file_upload", noun: "file", tooLarge: tooLarge, details: map[string]interface{}{"name": name},
	})
}
