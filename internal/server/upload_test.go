package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type chunkUploadResponse struct {
	UploadID   string `json:"upload_id"`
	ChunkSize  int64  `json:"chunk_size"`
	ChunkCount int    `json:"chunk_count"`
	Received   []int  `json:"received"`
}

func authenticatedRequest(t *testing.T, server *Server, token, method, target string, body *bytes.Reader) *http.Request {
	t.Helper()
	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, body)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	return request
}

func TestChunkUploadCompletesOutOfOrderAndReportsStatus(t *testing.T) {
	server, token := newTestServer(t)
	workingDir := t.TempDir()
	server.cfg.Terminal.WorkingDir = workingDir
	data := bytes.Repeat([]byte("x"), int(uploadChunkSize)+17)

	createBody := bytes.NewReader([]byte(`{"path":"","name":"chunked.bin","size":` + itoa64(int64(len(data))) + `}`))
	createRequest := authenticatedRequest(t, server, token, http.MethodPost, "/api/files/uploads", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %q", createResponse.Code, createResponse.Body.String())
	}
	var state chunkUploadResponse
	if err := json.Unmarshal(createResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.UploadID == "" || state.ChunkCount != 2 || state.ChunkSize != uploadChunkSize {
		t.Fatalf("unexpected create response: %+v", state)
	}

	uploadChunkForTest(t, server, token, state.UploadID, 1, data[uploadChunkSize:])

	statusRequest := authenticatedRequest(t, server, token, http.MethodGet, "/api/files/uploads/"+state.UploadID, bytes.NewReader(nil))
	statusResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %q", statusResponse.Code, statusResponse.Body.String())
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Received) != 1 || state.Received[0] != 1 {
		t.Fatalf("received chunks = %v", state.Received)
	}

	incompleteRequest := authenticatedRequest(t, server, token, http.MethodPost, "/api/files/uploads/"+state.UploadID+"/complete", bytes.NewReader(nil))
	incompleteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(incompleteResponse, incompleteRequest)
	if incompleteResponse.Code != http.StatusConflict {
		t.Fatalf("incomplete status = %d, body = %q", incompleteResponse.Code, incompleteResponse.Body.String())
	}

	uploadChunkForTest(t, server, token, state.UploadID, 0, data[:uploadChunkSize])
	completeRequest := authenticatedRequest(t, server, token, http.MethodPost, "/api/files/uploads/"+state.UploadID+"/complete", bytes.NewReader(nil))
	completeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(completeResponse, completeRequest)
	if completeResponse.Code != http.StatusCreated {
		t.Fatalf("complete status = %d, body = %q", completeResponse.Code, completeResponse.Body.String())
	}
	written, err := os.ReadFile(filepath.Join(workingDir, "chunked.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, data) {
		t.Fatal("completed file contents differ from uploaded chunks")
	}
}

func TestChunkUploadRejectsWrongChunkSizeAndCancelRemovesTemporaryFile(t *testing.T) {
	server, token := newTestServer(t)
	workingDir := t.TempDir()
	server.cfg.Terminal.WorkingDir = workingDir

	createBody := bytes.NewReader([]byte(`{"path":"","name":"cancel.bin","size":4}`))
	createRequest := authenticatedRequest(t, server, token, http.MethodPost, "/api/files/uploads", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %q", createResponse.Code, createResponse.Body.String())
	}
	var state chunkUploadResponse
	if err := json.Unmarshal(createResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	upload := server.findUpload(state.UploadID)
	if upload == nil {
		t.Fatal("upload task not found")
	}
	temporaryPath := upload.TemporaryPath

	wrongRequest := authenticatedRequest(t, server, token, http.MethodPut, "/api/files/uploads/"+state.UploadID+"/chunks/0", bytes.NewReader([]byte("bad")))
	wrongRequest.ContentLength = 3
	wrongResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongResponse, wrongRequest)
	if wrongResponse.Code != http.StatusBadRequest {
		t.Fatalf("wrong-size status = %d, body = %q", wrongResponse.Code, wrongResponse.Body.String())
	}

	cancelRequest := authenticatedRequest(t, server, token, http.MethodDelete, "/api/files/uploads/"+state.UploadID, bytes.NewReader(nil))
	cancelResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d, body = %q", cancelResponse.Code, cancelResponse.Body.String())
	}
	if _, err := os.Stat(temporaryPath); !os.IsNotExist(err) {
		t.Fatalf("temporary file still exists: %v", err)
	}
	if server.findUpload(state.UploadID) != nil {
		t.Fatal("cancelled upload task still exists")
	}
}

func uploadChunkForTest(t *testing.T, server *Server, token, uploadID string, index int, data []byte) {
	t.Helper()
	request := authenticatedRequest(t, server, token, http.MethodPut, "/api/files/uploads/"+uploadID+"/chunks/"+itoa(index), bytes.NewReader(data))
	request.ContentLength = int64(len(data))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("chunk %d status = %d, body = %q", index, response.Code, response.Body.String())
	}
}

func itoa(value int) string { return itoa64(int64(value)) }

func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}
