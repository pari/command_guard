package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PemFileInfo represents information about a PEM file
type PemFileInfo struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// PemFilesAPI handles PEM file operations
type PemFilesAPI struct {
	pemDir string
}

// NewPemFilesAPI creates a new PEM files API handler
func NewPemFilesAPI() *PemFilesAPI {
	return &PemFilesAPI{
		pemDir: "./pem_files",
	}
}

// ensurePemDir ensures the PEM files directory exists
func (pfa *PemFilesAPI) ensurePemDir() error {
	return os.MkdirAll(pfa.pemDir, privateDirMode)
}

// HandlePemFiles routes PEM file requests
func (pfa *PemFilesAPI) HandlePemFiles(w http.ResponseWriter, r *http.Request) {
	// Extract the path after /api/pemfiles/
	path := strings.TrimPrefix(r.URL.Path, "/api/pemfiles")
	path = strings.TrimPrefix(path, "/")

	switch r.Method {
	case "GET":
		if path == "" {
			// List PEM files
			pfa.HandleListPemFiles(w, r)
		} else {
			// Download specific PEM file
			pfa.HandleDownloadPemFile(w, r, path)
		}
	case "POST":
		// Upload PEM file
		pfa.HandleUploadPemFile(w, r)
	case "PUT":
		// Rename PEM file
		if strings.HasSuffix(r.URL.Path, "/rename") {
			// Extract filename from path (remove /rename suffix)
			fileName := strings.TrimSuffix(path, "/rename")
			if fileName != "" {
				pfa.HandleRenamePemFile(w, r, fileName)
			} else {
				sendConfigJSONError(w, http.StatusBadRequest, "File name required")
			}
		} else {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case "DELETE":
		// Delete PEM file
		if path != "" {
			pfa.HandleDeletePemFile(w, r, path)
		} else {
			sendConfigJSONError(w, http.StatusBadRequest, "File name required")
		}
	default:
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// HandleListPemFiles lists all PEM files
func (pfa *PemFilesAPI) HandleListPemFiles(w http.ResponseWriter, r *http.Request) {
	// Ensure directory exists
	if err := pfa.ensurePemDir(); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to access PEM files directory")
		return
	}

	// Read directory
	files, err := os.ReadDir(pfa.pemDir)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to list PEM files")
		return
	}

	// Build file info list
	pemFiles := []PemFileInfo{}
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".pem") {
			info, err := file.Info()
			if err != nil {
				continue
			}
			pemFiles = append(pemFiles, PemFileInfo{
				Name:     file.Name(),
				Size:     info.Size(),
				Modified: info.ModTime(),
			})
		}
	}

	// Send response
	sendConfigJSONSuccess(w, http.StatusOK, pemFiles, "PEM files retrieved successfully")
}

// pemUploadMaxBytes caps a PEM upload request (keys are a few KB).
const pemUploadMaxBytes = 1 << 20

// HandleUploadPemFile handles PEM file upload
func (pfa *PemFilesAPI) HandleUploadPemFile(w http.ResponseWriter, r *http.Request) {
	// ParseMultipartForm's argument only limits what is kept in memory; the rest
	// spills to temp files without bound. Cap the whole request body instead.
	r.Body = http.MaxBytesReader(w, r.Body, pemUploadMaxBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Failed to parse upload form")
		return
	}

	// Get the file from form
	file, header, err := r.FormFile("file")
	if err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Failed to get uploaded file")
		return
	}
	defer file.Close()

	// Get custom filename if provided, otherwise use original
	fileName := r.FormValue("filename")
	if fileName == "" {
		fileName = header.Filename
	}

	// Ensure .pem extension
	if !strings.HasSuffix(fileName, ".pem") {
		fileName += ".pem"
	}

	// Sanitize filename (remove path components)
	fileName = filepath.Base(fileName)

	// Ensure directory exists
	if err := pfa.ensurePemDir(); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to create PEM files directory")
		return
	}

	// Check if file already exists
	destPath := filepath.Join(pfa.pemDir, fileName)
	if _, err := os.Stat(destPath); err == nil {
		sendConfigJSONError(w, http.StatusConflict, fmt.Sprintf("A PEM file with the name '%s' already exists. Please choose a different name or delete the existing file first.", fileName))
		return
	}

	// Create destination file
	destFile, err := os.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to create PEM file")
		return
	}
	defer destFile.Close()

	// Copy uploaded file to destination
	if _, err := io.Copy(destFile, file); err != nil {
		os.Remove(destPath) // Clean up on error
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to save PEM file")
		return
	}

	// Private keys: readable by the service account only
	if err := os.Chmod(destPath, 0600); err != nil {
		// Log error but don't fail the upload
		fmt.Printf("Warning: Failed to set permissions on PEM file: %v\n", err)
	}

	// Get file info for response
	info, _ := os.Stat(destPath)
	fileInfo := PemFileInfo{
		Name:     fileName,
		Size:     info.Size(),
		Modified: info.ModTime(),
	}

	sendConfigJSONSuccess(w, http.StatusCreated, fileInfo, "PEM file uploaded successfully")
}

// HandleDownloadPemFile handles PEM file download
func (pfa *PemFilesAPI) HandleDownloadPemFile(w http.ResponseWriter, r *http.Request, fileName string) {
	// Sanitize filename
	fileName = filepath.Base(fileName)

	// Ensure .pem extension
	if !strings.HasSuffix(fileName, ".pem") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid PEM file name")
		return
	}

	// Build file path
	filePath := filepath.Join(pfa.pemDir, fileName)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		sendConfigJSONError(w, http.StatusNotFound, "PEM file not found")
		return
	}

	// Set headers for file download
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))

	// Serve the file
	http.ServeFile(w, r, filePath)
}

// HandleRenamePemFile handles PEM file renaming
func (pfa *PemFilesAPI) HandleRenamePemFile(w http.ResponseWriter, r *http.Request, oldFileName string) {
	// Parse request body
	var renameRequest struct {
		NewName string `json:"new_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&renameRequest); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Sanitize filenames
	oldFileName = filepath.Base(oldFileName)
	newFileName := filepath.Base(renameRequest.NewName)

	// Ensure .pem extension for both files
	if !strings.HasSuffix(oldFileName, ".pem") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid source PEM file name")
		return
	}

	if !strings.HasSuffix(newFileName, ".pem") {
		newFileName += ".pem"
	}

	// Check if new name is same as old name
	if oldFileName == newFileName {
		sendConfigJSONError(w, http.StatusBadRequest, "New name is the same as current name")
		return
	}

	// Build file paths
	oldPath := filepath.Join(pfa.pemDir, oldFileName)
	newPath := filepath.Join(pfa.pemDir, newFileName)

	// Check if source file exists
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		sendConfigJSONError(w, http.StatusNotFound, "PEM file not found")
		return
	}

	// Check if destination file already exists
	if _, err := os.Stat(newPath); err == nil {
		sendConfigJSONError(w, http.StatusConflict, "A file with the new name already exists")
		return
	}

	// Rename the file
	if err := os.Rename(oldPath, newPath); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to rename PEM file: %v", err))
		return
	}

	// Get file info for response
	info, _ := os.Stat(newPath)
	fileInfo := PemFileInfo{
		Name:     newFileName,
		Size:     info.Size(),
		Modified: info.ModTime(),
	}

	sendConfigJSONSuccess(w, http.StatusOK, fileInfo, "PEM file renamed successfully")
}

// HandleDeletePemFile handles PEM file deletion
func (pfa *PemFilesAPI) HandleDeletePemFile(w http.ResponseWriter, r *http.Request, fileName string) {
	// Sanitize filename
	fileName = filepath.Base(fileName)

	// Ensure .pem extension
	if !strings.HasSuffix(fileName, ".pem") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid PEM file name")
		return
	}

	// Build file path
	filePath := filepath.Join(pfa.pemDir, fileName)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		sendConfigJSONError(w, http.StatusNotFound, "PEM file not found")
		return
	}

	// Delete the file
	if err := os.Remove(filePath); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to delete PEM file")
		return
	}

	// Send success response
	response := map[string]string{
		"message": "PEM file deleted successfully",
		"file":    fileName,
	}

	jsonResponse, _ := json.Marshal(map[string]interface{}{
		"success": true,
		"data":    response,
		"error":   "PEM file deleted successfully",
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(jsonResponse)
}
