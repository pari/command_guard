package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/scrypt"
)

const (
	backupFormatName       = "commandguard-config-backup"
	backupEnvelopeFormat   = "commandguard-encrypted-backup"
	backupFormatVersion    = 1
	backupMaxPasswordBytes = 1024
	backupMinPasswordLen   = 12
	backupMaxPayloadBytes  = 100 << 20
	backupScryptN          = 32768
	backupScryptR          = 8
	backupScryptP          = 1
	// Upper bounds accepted from an imported file (at most ~128 MB of scrypt memory).
	backupScryptMaxN    = 131072
	backupScryptMaxR    = 8
	backupScryptMaxP    = 4
	backupGCMNonceBytes = 12
	backupKeyBytes      = 32
)

var configFileMutex sync.Mutex

type BackupAPI struct {
	configPath string
	pemDir     string
}

type backupPasswordRequest struct {
	Password string `json:"password"`
}

type encryptedBackupEnvelope struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	ScryptN    int    `json:"scrypt_n"`
	ScryptR    int    `json:"scrypt_r"`
	ScryptP    int    `json:"scrypt_p"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type backupManifest struct {
	Format        string                    `json:"format"`
	FormatVersion int                       `json:"format_version"`
	AppVersion    string                    `json:"app_version"`
	CreatedAt     string                    `json:"created_at"`
	Files         map[string]backupFileInfo `json:"files"`
	Counts        backupCounts              `json:"counts"`
}

type backupFileInfo struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type backupCounts struct {
	Hosts        int `json:"hosts"`
	Commands     int `json:"commands"`
	RunBooks     int `json:"runbooks"`
	HTTPRequests int `json:"http_requests"`
	Users        int `json:"users"`
	PEMFiles     int `json:"pem_files"`
}

type backupPayload struct {
	Manifest backupManifest
	Config   []byte
	EmailTpl []byte
	HasEmail bool
	PEMFiles map[string][]byte
}

type backupInspectResponse struct {
	BackupAppVersion  string       `json:"backup_app_version"`
	CurrentAppVersion string       `json:"current_app_version"`
	CreatedAt         string       `json:"created_at"`
	Compatible        bool         `json:"compatible"`
	Counts            backupCounts `json:"counts"`
	IncludesEmailTpl  bool         `json:"includes_email_tpl"`
	RestoreMode       string       `json:"restore_mode"`
	RequiresRestart   bool         `json:"requires_restart"`
	PEMAdded          []string     `json:"pem_added"`
	PEMUpdated        []string     `json:"pem_updated"`
	PEMDeleted        []string     `json:"pem_deleted"`
	PEMUnchanged      []string     `json:"pem_unchanged"`
}

func NewBackupAPI(configPath string) *BackupAPI {
	return &BackupAPI{configPath: configPath, pemDir: "./pem_files"}
}

func (ba *BackupAPI) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req backupPasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, backupMaxPasswordBytes+128)).Decode(&req); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid backup password request")
		return
	}
	if err := validateBackupPassword(req.Password); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload, err := ba.buildPayload()
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	zipData, err := encodeBackupZip(payload)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to create backup archive")
		return
	}
	encrypted, err := encryptBackup(zipData, req.Password)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to encrypt backup")
		return
	}

	fileName := fmt.Sprintf("commandguard_%s_%s.cgbak", AppVersion, time.Now().UTC().Format("20060102_150405"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.Header().Set("Content-Length", strconv.Itoa(len(encrypted)))
	_, _ = w.Write(encrypted)
}

func (ba *BackupAPI) HandleInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := ba.readPayloadFromMultipart(w, r)
	if err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ba.validatePayload(payload, false); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sendConfigJSONSuccess(w, http.StatusOK, ba.inspectPayload(payload), "Backup inspected successfully")
}

func (ba *BackupAPI) HandleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := ba.readPayloadFromMultipart(w, r)
	if err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	confirm := strings.TrimSpace(r.FormValue("confirm"))
	if confirm != "true" && confirm != "RESTORE" {
		sendConfigJSONError(w, http.StatusBadRequest, "Restore confirmation is required")
		return
	}
	if err := ba.validatePayload(payload, true); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := configRestoreGate.BeginRestore(); err != nil {
		sendConfigJSONError(w, http.StatusConflict, err.Error())
		return
	}
	defer configRestoreGate.EndRestore()

	configFileMutex.Lock()
	defer configFileMutex.Unlock()

	requiresRestart := ba.restoreRequiresRestart(payload.Config)
	if err := ba.restorePayload(payload); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"restored":         true,
		"requires_restart": requiresRestart,
		"auth_restored":    true,
	}, "Backup restored successfully")
}

func validateBackupPassword(password string) error {
	if len(password) < backupMinPasswordLen {
		return fmt.Errorf("Backup password must be at least %d characters", backupMinPasswordLen)
	}
	if len(password) > backupMaxPasswordBytes {
		return fmt.Errorf("Backup password is too long")
	}
	return nil
}

func (ba *BackupAPI) buildPayload() (*backupPayload, error) {
	configData, err := ioutil.ReadFile(ba.configPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to read configuration")
	}
	var config Config
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("Failed to parse configuration")
	}

	payload := &backupPayload{
		Config:   configData,
		PEMFiles: make(map[string][]byte),
	}
	if emailData, err := ioutil.ReadFile("email.tpl"); err == nil {
		payload.EmailTpl = emailData
		payload.HasEmail = true
	}

	entries, err := ioutil.ReadDir(ba.pemDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pem") {
				continue
			}
			data, err := ioutil.ReadFile(filepath.Join(ba.pemDir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("Failed to read PEM file %s", entry.Name())
			}
			payload.PEMFiles[entry.Name()] = data
		}
	}

	payload.Manifest = backupManifest{
		Format:        backupFormatName,
		FormatVersion: backupFormatVersion,
		AppVersion:    AppVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Files:         make(map[string]backupFileInfo),
		Counts: backupCounts{
			Hosts:        len(config.Hosts),
			Commands:     len(config.Commands),
			RunBooks:     len(config.RunBooks),
			HTTPRequests: len(config.HTTPRequests),
			Users:        len(config.Users),
			PEMFiles:     len(payload.PEMFiles),
		},
	}
	payload.Manifest.Files["config.json"] = fileInfo(configData)
	if payload.HasEmail {
		payload.Manifest.Files["email.tpl"] = fileInfo(payload.EmailTpl)
	}
	for name, data := range payload.PEMFiles {
		payload.Manifest.Files["pem_files/"+name] = fileInfo(data)
	}
	return payload, nil
}

func fileInfo(data []byte) backupFileInfo {
	sum := sha256.Sum256(data)
	return backupFileInfo{Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sum[:])}
}

func encodeBackupZip(payload *backupPayload) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	if err := addZipFile(zw, "manifest.json", mustJSON(payload.Manifest)); err != nil {
		return nil, err
	}
	if err := addZipFile(zw, "config.json", payload.Config); err != nil {
		return nil, err
	}
	if payload.HasEmail {
		if err := addZipFile(zw, "email.tpl", payload.EmailTpl); err != nil {
			return nil, err
		}
	}
	names := sortedKeys(payload.PEMFiles)
	for _, name := range names {
		if err := addZipFile(zw, "pem_files/"+name, payload.PEMFiles[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > backupMaxPayloadBytes {
		return nil, fmt.Errorf("backup payload exceeds %d bytes", backupMaxPayloadBytes)
	}
	return buf.Bytes(), nil
}

func mustJSON(value interface{}) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	_ = enc.Encode(value)
	return buf.Bytes()
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0600)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func encryptBackup(plain []byte, password string) ([]byte, error) {
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(password), salt, backupScryptN, backupScryptR, backupScryptP, backupKeyBytes)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	envelope := encryptedBackupEnvelope{
		Format:     backupEnvelopeFormat,
		Version:    backupFormatVersion,
		KDF:        "scrypt",
		ScryptN:    backupScryptN,
		ScryptR:    backupScryptR,
		ScryptP:    backupScryptP,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, []byte(backupEnvelopeFormat))),
	}
	return mustJSON(envelope), nil
}

func decryptBackup(data []byte, password string) ([]byte, error) {
	var envelope encryptedBackupEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("invalid encrypted backup file")
	}
	if envelope.Format != backupEnvelopeFormat || envelope.Version != backupFormatVersion || envelope.KDF != "scrypt" {
		return nil, fmt.Errorf("unsupported backup format")
	}
	// The file is untrusted until decrypted: cap the key-derivation cost it can
	// request (memory is about 128 * N * r bytes) so a crafted backup cannot
	// exhaust the server before its password is even checked.
	if envelope.ScryptN < 2 || envelope.ScryptN > backupScryptMaxN || envelope.ScryptN&(envelope.ScryptN-1) != 0 ||
		envelope.ScryptR < 1 || envelope.ScryptR > backupScryptMaxR ||
		envelope.ScryptP < 1 || envelope.ScryptP > backupScryptMaxP {
		return nil, fmt.Errorf("invalid backup key settings")
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.Salt)
	if err != nil {
		return nil, fmt.Errorf("invalid backup salt")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("invalid backup nonce")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("invalid backup ciphertext")
	}
	if len(ciphertext) > backupMaxPayloadBytes+1024 {
		return nil, fmt.Errorf("backup file is too large")
	}
	if len(salt) < 8 || len(salt) > 64 {
		return nil, fmt.Errorf("invalid backup salt")
	}
	if len(nonce) != backupGCMNonceBytes { // gcm.Open panics on any other length
		return nil, fmt.Errorf("invalid backup nonce")
	}
	key, err := scrypt.Key([]byte(password), salt, envelope.ScryptN, envelope.ScryptR, envelope.ScryptP, backupKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to derive backup key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize backup cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize backup cipher")
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(backupEnvelopeFormat))
	if err != nil {
		return nil, fmt.Errorf("invalid backup password or corrupted backup file")
	}
	if len(plain) > backupMaxPayloadBytes {
		return nil, fmt.Errorf("backup payload is too large")
	}
	return plain, nil
}

func (ba *BackupAPI) readPayloadFromMultipart(w http.ResponseWriter, r *http.Request) (*backupPayload, error) {
	r.Body = http.MaxBytesReader(w, r.Body, backupMaxPayloadBytes*2)
	if err := r.ParseMultipartForm(backupMaxPayloadBytes * 2); err != nil {
		return nil, fmt.Errorf("invalid multipart backup request")
	}
	password := r.FormValue("password")
	if err := validateBackupPassword(password); err != nil {
		return nil, err
	}
	file, _, err := r.FormFile("backup")
	if err != nil {
		return nil, fmt.Errorf("backup file is required")
	}
	defer file.Close()
	data, err := readMultipartFileLimited(file, backupMaxPayloadBytes*2)
	if err != nil {
		return nil, err
	}
	zipData, err := decryptBackup(data, password)
	if err != nil {
		return nil, err
	}
	return decodeBackupZip(zipData)
}

func readMultipartFileLimited(file multipart.File, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(file, limit+1)); err != nil {
		return nil, fmt.Errorf("failed to read backup file")
	}
	if int64(buf.Len()) > limit {
		return nil, fmt.Errorf("backup file is too large")
	}
	return buf.Bytes(), nil
}

func decodeBackupZip(data []byte) (*backupPayload, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid backup archive")
	}
	payload := &backupPayload{PEMFiles: make(map[string][]byte)}
	seen := make(map[string]bool)
	var total int64
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if !validBackupPath(name) {
			return nil, fmt.Errorf("backup contains invalid path: %s", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("backup contains duplicate path: %s", name)
		}
		seen[name] = true
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to read backup archive")
		}
		content, err := io.ReadAll(io.LimitReader(rc, backupMaxPayloadBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read backup archive")
		}
		total += int64(len(content))
		if total > backupMaxPayloadBytes {
			return nil, fmt.Errorf("backup payload is too large")
		}
		switch {
		case name == "manifest.json":
			if err := json.Unmarshal(content, &payload.Manifest); err != nil {
				return nil, fmt.Errorf("invalid backup manifest")
			}
		case name == "config.json":
			payload.Config = content
		case name == "email.tpl":
			payload.EmailTpl = content
			payload.HasEmail = true
		case strings.HasPrefix(name, "pem_files/"):
			base := strings.TrimPrefix(name, "pem_files/")
			if !validPEMBackupName(base) {
				return nil, fmt.Errorf("backup contains invalid PEM file name: %s", base)
			}
			payload.PEMFiles[base] = content
		}
	}
	if payload.Manifest.Format == "" {
		return nil, fmt.Errorf("backup manifest is missing")
	}
	if len(payload.Config) == 0 {
		return nil, fmt.Errorf("backup config.json is missing")
	}
	return payload, nil
}

func validBackupPath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "../") || strings.Contains(name, "\\") {
		return false
	}
	return name == "manifest.json" || name == "config.json" || name == "email.tpl" ||
		(strings.HasPrefix(name, "pem_files/") && validPEMBackupName(strings.TrimPrefix(name, "pem_files/")))
}

func validPEMBackupName(name string) bool {
	if name == "" || name != filepath.Base(name) || !strings.EqualFold(filepath.Ext(name), ".pem") {
		return false
	}
	for _, r := range name {
		if !(r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
			return false
		}
	}
	return true
}

func (ba *BackupAPI) validatePayload(payload *backupPayload, enforceVersion bool) error {
	if payload.Manifest.Format != backupFormatName || payload.Manifest.FormatVersion != backupFormatVersion {
		return fmt.Errorf("unsupported backup payload format")
	}
	if enforceVersion && compareAppVersions(AppVersion, payload.Manifest.AppVersion) < 0 {
		return fmt.Errorf("backup was created by newer app version %s; current app version is %s", payload.Manifest.AppVersion, AppVersion)
	}
	if err := verifyPayloadChecksums(payload); err != nil {
		return err
	}
	return validateRestoredConfig(payload.Config, payload.HasEmail, payload.PEMFiles)
}

func verifyPayloadChecksums(payload *backupPayload) error {
	expected := map[string][]byte{"config.json": payload.Config}
	if payload.HasEmail {
		expected["email.tpl"] = payload.EmailTpl
	}
	for name, data := range payload.PEMFiles {
		expected["pem_files/"+name] = data
	}
	if len(payload.Manifest.Files) != len(expected) {
		return fmt.Errorf("backup manifest file list does not match archive contents")
	}
	for name, data := range expected {
		info, ok := payload.Manifest.Files[name]
		if !ok {
			return fmt.Errorf("backup manifest is missing %s", name)
		}
		actual := fileInfo(data)
		if info.Size != actual.Size || !strings.EqualFold(info.SHA256, actual.SHA256) {
			return fmt.Errorf("backup checksum mismatch for %s", name)
		}
	}
	return nil
}

func validateRestoredConfig(configData []byte, hasEmailTpl bool, pemFiles map[string][]byte) error {
	if err := validateNoLegacyWorkflowFields(configData); err != nil {
		return err
	}
	var tempConfig struct {
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.Unmarshal(configData, &tempConfig); err != nil {
		return fmt.Errorf("invalid config JSON: %v", err)
	}
	substituted, err := substituteVariables(string(configData), tempConfig.Variables)
	if err != nil {
		return fmt.Errorf("variable substitution failed: %v", err)
	}
	var config Config
	if err := json.Unmarshal([]byte(substituted), &config); err != nil {
		return fmt.Errorf("failed to parse restored config: %v", err)
	}
	if config.ScheduledJobsOverviewEmail != nil {
		normalized := normalizeScheduledJobsOverviewEmailConfig(config.ScheduledJobsOverviewEmail)
		if err := validateScheduledJobsOverviewEmailConfig(normalized, config.SMTP, false, config.Variables); err != nil {
			return fmt.Errorf("invalid Scheduled Jobs Overview email configuration: %v", err)
		}
	}
	if config.Port <= 0 || config.Port > 65535 {
		return fmt.Errorf("invalid service port: %d", config.Port)
	}
	if problems, _ := hostKeyConfigProblems(&config); len(problems) > 0 {
		return fmt.Errorf("%s", problems[0])
	}
	if err := resolveBastionHosts(config.Hosts); err != nil {
		return fmt.Errorf("invalid hosts configuration: %v", err)
	}
	if err := validateVNCSettings(config.VNC); err != nil {
		return err
	}
	if err := validateVNCConnections(config.VNCConnections, config.Hosts); err != nil {
		return fmt.Errorf("invalid vnc_connections: %v", err)
	}
	if config.DiskMonitor != nil {
		normalizedDisk := normalizeDiskMonitorConfig(config.DiskMonitor)
		if err := validateDiskMonitorConfig(normalizedDisk, config.Hosts); err != nil {
			return fmt.Errorf("invalid disk_monitor configuration: %v", err)
		}
	}
	cmdIDs := make(map[string]bool)
	hasEmailNotifications := false
	for _, cmd := range config.Commands {
		if cmd.CmdID == "" {
			return fmt.Errorf("command with empty cmd_id is not allowed")
		}
		if cmdIDs[cmd.CmdID] {
			return fmt.Errorf("duplicate command id '%s'", cmd.CmdID)
		}
		cmdIDs[cmd.CmdID] = true
		if _, exists := config.Hosts[cmd.HostRef]; !exists {
			return fmt.Errorf("command '%s' references non-existent host '%s'", cmd.CmdID, cmd.HostRef)
		}
		if cmd.LogStorage != "" && !strings.EqualFold(cmd.LogStorage, "local") && !strings.EqualFold(cmd.LogStorage, "s3") {
			return fmt.Errorf("command '%s' has invalid log_storage '%s'", cmd.CmdID, cmd.LogStorage)
		}
		if cmd.CronSchedule != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(cmd.CronSchedule); err != nil {
				return fmt.Errorf("command '%s' has invalid cron schedule: %v", cmd.CmdID, err)
			}
		}
		if cmd.MaxOutputSize != "" {
			if _, err := parseSize(cmd.MaxOutputSize); err != nil {
				return fmt.Errorf("command '%s' has invalid max_output_size: %v", cmd.CmdID, err)
			}
		}
		if cmd.NotifyEmail != nil {
			hasEmailNotifications = true
		}
	}
	for hostName, host := range config.Hosts {
		normalizeHost(&host)
		if strings.ToLower(host.ConnectionType) == "winrm" {
			if host.Username == "" || host.Password == "" {
				return fmt.Errorf("host '%s' (WinRM) requires username and password", hostName)
			}
			if host.WinRMCACert != "" {
				if _, err := os.Stat(host.WinRMCACert); os.IsNotExist(err) {
					return fmt.Errorf("host '%s' references non-existent CA certificate: %s", hostName, host.WinRMCACert)
				}
			}
		} else {
			if host.Password == "" && host.PemFile == "" {
				return fmt.Errorf("host '%s' has no authentication method (neither password nor PEM key)", hostName)
			}
			if host.PemFile != "" && !restoredPathExists(host.PemFile, pemFiles) {
				return fmt.Errorf("host '%s' references non-existent PEM file: %s", hostName, host.PemFile)
			}
		}
		if host.Port <= 0 || host.Port > 65535 {
			return fmt.Errorf("host '%s' has invalid port: %d", hostName, host.Port)
		}
	}
	requestIDs, err := validateHTTPRequests(config.HTTPRequests, cmdIDs)
	if err != nil {
		return err
	}
	for _, req := range config.HTTPRequests {
		if req.NotifyEmail != nil {
			hasEmailNotifications = true
		}
	}
	if err := validateRunBooks(config.RunBooks, cmdIDs, requestIDs); err != nil {
		return err
	}
	for _, runBook := range config.RunBooks {
		if runBook.CronSchedule != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(runBook.CronSchedule); err != nil {
				return fmt.Errorf("RunBook '%s' has invalid cron schedule: %v", runBook.RunBookID, err)
			}
		}
		if runBook.NotifyEmail != nil {
			hasEmailNotifications = true
		}
	}
	if hasEmailNotifications && config.SMTP != nil && !hasEmailTpl {
		return fmt.Errorf("email notifications are configured but email.tpl is missing from the backup")
	}
	return nil
}

func restoredPathExists(path string, pemFiles map[string][]byte) bool {
	clean := filepath.Clean(path)
	if strings.HasPrefix(filepath.ToSlash(clean), "pem_files/") || strings.HasPrefix(filepath.ToSlash(clean), "./pem_files/") {
		_, ok := pemFiles[filepath.Base(clean)]
		return ok
	}
	_, err := os.Stat(path)
	return err == nil
}

func compareAppVersions(current, backup string) int {
	left := parseVersionParts(current)
	right := parseVersionParts(backup)
	for i := 0; i < len(left); i++ {
		if left[i] > right[i] {
			return 1
		}
		if left[i] < right[i] {
			return -1
		}
	}
	return 0
}

func parseVersionParts(version string) [3]int {
	var result [3]int
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		value, _ := strconv.Atoi(parts[i])
		result[i] = value
	}
	return result
}

func (ba *BackupAPI) inspectPayload(payload *backupPayload) backupInspectResponse {
	current := ba.currentPEMHashes()
	backupHashes := make(map[string]string)
	for name, data := range payload.PEMFiles {
		backupHashes[name] = fileInfo(data).SHA256
	}
	var added, updated, deleted, unchanged []string
	for name, hash := range backupHashes {
		if currentHash, ok := current[name]; !ok {
			added = append(added, name)
		} else if currentHash == hash {
			unchanged = append(unchanged, name)
		} else {
			updated = append(updated, name)
		}
	}
	for name := range current {
		if _, ok := backupHashes[name]; !ok {
			deleted = append(deleted, name)
		}
	}
	sort.Strings(added)
	sort.Strings(updated)
	sort.Strings(deleted)
	sort.Strings(unchanged)
	return backupInspectResponse{
		BackupAppVersion:  payload.Manifest.AppVersion,
		CurrentAppVersion: AppVersion,
		CreatedAt:         payload.Manifest.CreatedAt,
		Compatible:        compareAppVersions(AppVersion, payload.Manifest.AppVersion) >= 0,
		Counts:            payload.Manifest.Counts,
		IncludesEmailTpl:  payload.HasEmail,
		RestoreMode:       "exact_snapshot",
		RequiresRestart:   ba.restoreRequiresRestart(payload.Config),
		PEMAdded:          added,
		PEMUpdated:        updated,
		PEMDeleted:        deleted,
		PEMUnchanged:      unchanged,
	}
}

func (ba *BackupAPI) currentPEMHashes() map[string]string {
	result := make(map[string]string)
	entries, err := ioutil.ReadDir(ba.pemDir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pem") {
			continue
		}
		data, err := ioutil.ReadFile(filepath.Join(ba.pemDir, entry.Name()))
		if err == nil {
			result[entry.Name()] = fileInfo(data).SHA256
		}
	}
	return result
}

func (ba *BackupAPI) restoreRequiresRestart(newConfigData []byte) bool {
	var current Config
	if data, err := ioutil.ReadFile(ba.configPath); err == nil {
		_ = json.Unmarshal(data, &current)
	}
	var restored Config
	_ = json.Unmarshal(newConfigData, &restored)
	return current.Port != restored.Port || current.BindAddress != restored.BindAddress
}

func (ba *BackupAPI) restorePayload(payload *backupPayload) error {
	rollback, err := ba.captureRollback()
	if err != nil {
		return err
	}
	configData, restoredConfig, err := normalizeConfigForRestore(payload.Config)
	if err != nil {
		return err
	}
	if err := ba.applySnapshot(configData, payload); err != nil {
		_ = ba.applyRollback(rollback)
		return err
	}
	if err := cache.loadIfChanged(); err != nil {
		_ = ba.applyRollback(rollback)
		_ = cache.loadIfChanged()
		return fmt.Errorf("restore failed validation after applying files: %v", err)
	}
	// The file may reference secrets as ${{ENV}} or {{variable}}. Use the values
	// the loader resolved, not the literal placeholders, or the placeholder text
	// itself would become the admin password / JWT signing key.
	if resolved := cache.GetConfig(); resolved != nil {
		restoredConfig = *resolved
	}
	if authManager != nil {
		authManager.RestoreCredentials(AuthConfig{
			AdminPassword:  restoredConfig.AdminPassword,
			TOTPSecret:     restoredConfig.TOTPSecret,
			JWTSecret:      restoredConfig.JWTSecret,
			DevMode:        restoredConfig.DevMode,
			Users:          restoredConfig.Users,
			TrustedProxies: restoredConfig.TrustedProxies,
		})
	}
	return nil
}

type backupRollback struct {
	Config      []byte
	EmailTpl    []byte
	HasEmailTpl bool
	PEMFiles    map[string][]byte
}

func (ba *BackupAPI) captureRollback() (*backupRollback, error) {
	configData, err := ioutil.ReadFile(ba.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read current configuration for rollback")
	}
	rollback := &backupRollback{Config: configData, PEMFiles: make(map[string][]byte)}
	if emailData, err := ioutil.ReadFile("email.tpl"); err == nil {
		rollback.EmailTpl = emailData
		rollback.HasEmailTpl = true
	}
	entries, err := ioutil.ReadDir(ba.pemDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pem") {
				continue
			}
			data, err := ioutil.ReadFile(filepath.Join(ba.pemDir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("failed to read current PEM files for rollback")
			}
			rollback.PEMFiles[entry.Name()] = data
		}
	}
	return rollback, nil
}

func normalizeConfigForRestore(data []byte) ([]byte, Config, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, Config{}, fmt.Errorf("invalid restored config JSON")
	}
	stampConfigVersion(raw)
	mapData, err := json.Marshal(raw)
	if err != nil {
		return nil, Config{}, err
	}
	var config Config
	if err := json.Unmarshal(mapData, &config); err != nil {
		return nil, Config{}, fmt.Errorf("invalid restored config structure")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(config); err != nil {
		return nil, Config{}, err
	}
	return buf.Bytes(), config, nil
}

func (ba *BackupAPI) applySnapshot(configData []byte, payload *backupPayload) error {
	if err := os.MkdirAll(ba.pemDir, privateDirMode); err != nil {
		return fmt.Errorf("failed to create PEM directory")
	}
	if err := writeFileAtomic(ba.configPath, configData, 0600); err != nil {
		return fmt.Errorf("failed to restore configuration")
	}
	if payload.HasEmail {
		if err := writeFileAtomic("email.tpl", payload.EmailTpl, 0644); err != nil {
			return fmt.Errorf("failed to restore email template")
		}
	} else if err := os.Remove("email.tpl"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove email template")
	}
	current, _ := ioutil.ReadDir(ba.pemDir)
	keep := make(map[string]bool)
	for name := range payload.PEMFiles {
		keep[name] = true
	}
	for _, entry := range current {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pem") {
			continue
		}
		if !keep[entry.Name()] {
			if err := os.Remove(filepath.Join(ba.pemDir, entry.Name())); err != nil {
				return fmt.Errorf("failed to remove old PEM file %s", entry.Name())
			}
		}
	}
	for _, name := range sortedKeys(payload.PEMFiles) {
		if err := writeFileAtomic(filepath.Join(ba.pemDir, name), payload.PEMFiles[name], 0600); err != nil {
			return fmt.Errorf("failed to restore PEM file %s", name)
		}
	}
	return nil
}

func (ba *BackupAPI) applyRollback(rollback *backupRollback) error {
	_ = os.MkdirAll(ba.pemDir, privateDirMode)
	if err := writeFileAtomic(ba.configPath, rollback.Config, 0600); err != nil {
		return err
	}
	if rollback.HasEmailTpl {
		if err := writeFileAtomic("email.tpl", rollback.EmailTpl, 0644); err != nil {
			return err
		}
	} else {
		_ = os.Remove("email.tpl")
	}
	current, _ := ioutil.ReadDir(ba.pemDir)
	keep := make(map[string]bool)
	for name := range rollback.PEMFiles {
		keep[name] = true
	}
	for _, entry := range current {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pem") {
			continue
		}
		if !keep[entry.Name()] {
			_ = os.Remove(filepath.Join(ba.pemDir, entry.Name()))
		}
	}
	for name, data := range rollback.PEMFiles {
		if err := writeFileAtomic(filepath.Join(ba.pemDir, name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := ioutil.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
