package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	uploadChunkSize   = int64(8 << 20)
	maximumUploadSize = int64(512 << 20)
	uploadLifetime    = 24 * time.Hour
)

type chunkUpload struct {
	mu            sync.Mutex
	ID            string
	FinalPath     string
	TemporaryPath string
	Size          int64
	ChunkSize     int64
	ChunkCount    int
	Received      []bool
	UpdatedAt     time.Time
}

type createChunkUploadRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (server *Server) createChunkUpload(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 16<<10)
	var input createChunkUploadRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		http.Error(writer, "invalid upload request", http.StatusBadRequest)
		return
	}
	if input.Size < 0 || input.Size > maximumUploadSize {
		http.Error(writer, "文件超过 512 MiB 上传限制", http.StatusRequestEntityTooLarge)
		return
	}
	name := filepath.Base(input.Name)
	if name == "." || name == string(filepath.Separator) || name != input.Name {
		http.Error(writer, "invalid filename", http.StatusBadRequest)
		return
	}
	directory, err := server.filePath(input.Path)
	if err != nil {
		http.Error(writer, "invalid path", http.StatusBadRequest)
		return
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		http.Error(writer, "upload directory not found", http.StatusBadRequest)
		return
	}
	finalPath := filepath.Join(directory, name)
	if _, err := os.Stat(finalPath); err == nil {
		http.Error(writer, "file already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	destination, err := os.CreateTemp(directory, ".termdock-upload-*")
	if err != nil {
		http.Error(writer, "cannot create upload file", http.StatusInternalServerError)
		return
	}
	temporaryPath := destination.Name()
	prepared := false
	defer func() {
		_ = destination.Close()
		if !prepared {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := destination.Chmod(0o600); err != nil {
		http.Error(writer, "cannot prepare upload file", http.StatusInternalServerError)
		return
	}
	if err := destination.Truncate(input.Size); err != nil {
		http.Error(writer, "cannot prepare upload file", http.StatusInternalServerError)
		return
	}
	if err := destination.Close(); err != nil {
		http.Error(writer, "cannot prepare upload file", http.StatusInternalServerError)
		return
	}
	id, err := newUploadID()
	if err != nil {
		http.Error(writer, "cannot create upload", http.StatusInternalServerError)
		return
	}
	chunkCount := int((input.Size + uploadChunkSize - 1) / uploadChunkSize)
	upload := &chunkUpload{ID: id, FinalPath: finalPath, TemporaryPath: temporaryPath, Size: input.Size, ChunkSize: uploadChunkSize, ChunkCount: chunkCount, Received: make([]bool, chunkCount), UpdatedAt: time.Now()}
	server.uploadMu.Lock()
	server.cleanupExpiredUploadsLocked()
	server.uploads[id] = upload
	server.uploadMu.Unlock()
	prepared = true
	writeJSON(writer, map[string]any{"upload_id": id, "chunk_size": uploadChunkSize, "chunk_count": chunkCount})
}

func (server *Server) chunkUploadStatus(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	upload := server.findUpload(request.PathValue("id"))
	if upload == nil {
		http.Error(writer, "upload not found", http.StatusNotFound)
		return
	}
	upload.mu.Lock()
	received := make([]int, 0, upload.ChunkCount)
	for index, complete := range upload.Received {
		if complete {
			received = append(received, index)
		}
	}
	upload.UpdatedAt = time.Now()
	upload.mu.Unlock()
	writeJSON(writer, map[string]any{"upload_id": upload.ID, "chunk_size": upload.ChunkSize, "chunk_count": upload.ChunkCount, "received": received})
}

func (server *Server) uploadChunk(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	upload := server.findUpload(request.PathValue("id"))
	if upload == nil {
		http.Error(writer, "upload not found", http.StatusNotFound)
		return
	}
	index, err := strconv.Atoi(request.PathValue("index"))
	if err != nil || index < 0 || index >= upload.ChunkCount {
		http.Error(writer, "invalid chunk index", http.StatusBadRequest)
		return
	}
	expected := upload.ChunkSize
	if index == upload.ChunkCount-1 {
		expected = upload.Size - int64(index)*upload.ChunkSize
	}
	if request.ContentLength >= 0 && request.ContentLength != expected {
		http.Error(writer, "invalid chunk size", http.StatusBadRequest)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, upload.ChunkSize)
	data, err := io.ReadAll(request.Body)
	if err != nil || int64(len(data)) != expected {
		http.Error(writer, "incomplete chunk", http.StatusBadRequest)
		return
	}
	upload.mu.Lock()
	defer upload.mu.Unlock()
	file, err := os.OpenFile(upload.TemporaryPath, os.O_WRONLY, 0)
	if err != nil {
		http.Error(writer, "upload no longer available", http.StatusGone)
		return
	}
	_, writeErr := file.WriteAt(data, int64(index)*upload.ChunkSize)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		http.Error(writer, "cannot store chunk", http.StatusInternalServerError)
		return
	}
	upload.Received[index] = true
	upload.UpdatedAt = time.Now()
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) completeChunkUpload(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := request.PathValue("id")
	upload := server.findUpload(id)
	if upload == nil {
		http.Error(writer, "upload not found", http.StatusNotFound)
		return
	}
	upload.mu.Lock()
	for _, received := range upload.Received {
		if !received {
			upload.mu.Unlock()
			http.Error(writer, "upload is incomplete", http.StatusConflict)
			return
		}
	}
	if _, err := os.Stat(upload.FinalPath); err == nil {
		upload.mu.Unlock()
		http.Error(writer, "file already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		upload.mu.Unlock()
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	file, err := os.OpenFile(upload.TemporaryPath, os.O_RDWR, 0)
	if err != nil {
		upload.mu.Unlock()
		http.Error(writer, "upload no longer available", http.StatusGone)
		return
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		upload.mu.Unlock()
		http.Error(writer, "cannot finish upload", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(upload.TemporaryPath, upload.FinalPath); err != nil {
		upload.mu.Unlock()
		http.Error(writer, "cannot finish upload", http.StatusInternalServerError)
		return
	}
	upload.mu.Unlock()
	server.uploadMu.Lock()
	if server.uploads[id] == upload {
		delete(server.uploads, id)
	}
	server.uploadMu.Unlock()
	writer.WriteHeader(http.StatusCreated)
}

func (server *Server) cancelChunkUpload(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := request.PathValue("id")
	server.uploadMu.Lock()
	upload := server.uploads[id]
	delete(server.uploads, id)
	server.uploadMu.Unlock()
	if upload != nil {
		upload.mu.Lock()
		err := os.Remove(upload.TemporaryPath)
		upload.mu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(writer, "cannot cancel upload", http.StatusInternalServerError)
			return
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) findUpload(id string) *chunkUpload {
	server.uploadMu.Lock()
	defer server.uploadMu.Unlock()
	server.cleanupExpiredUploadsLocked()
	return server.uploads[id]
}

func (server *Server) cleanupExpiredUploadsLocked() {
	cutoff := time.Now().Add(-uploadLifetime)
	for id, upload := range server.uploads {
		upload.mu.Lock()
		expired := upload.UpdatedAt.Before(cutoff)
		if expired {
			_ = os.Remove(upload.TemporaryPath)
			delete(server.uploads, id)
		}
		upload.mu.Unlock()
	}
}

func newUploadID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate upload id: %w", err)
	}
	return hex.EncodeToString(data), nil
}
