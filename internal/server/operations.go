package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type fileOperationRequest struct {
	Action      string   `json:"action"`
	Paths       []string `json:"paths"`
	Path        string   `json:"path"`
	Destination string   `json:"destination"`
	Name        string   `json:"name"`
}

func (server *Server) operationPath(requested string) (string, error) {
	return server.filePath(requested)
}

func (server *Server) fileOperation(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "未登录", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	var operation fileOperationRequest
	if err := json.NewDecoder(request.Body).Decode(&operation); err != nil {
		http.Error(writer, "请求格式无效", http.StatusBadRequest)
		return
	}
	var err error
	switch operation.Action {
	case "rename":
		err = server.renamePath(operation.Path, operation.Name)
	case "copy", "move":
		err = server.transferPaths(operation.Paths, operation.Destination, operation.Action == "move")
	case "delete":
		err = server.deletePaths(operation.Paths)
	default:
		http.Error(writer, "不支持的文件操作", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) renamePath(source, name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return errors.New("名称无效")
	}
	sourcePath, err := server.operationPath(source)
	if err != nil {
		return err
	}
	if err := server.ensureNotRoot(sourcePath); err != nil {
		return err
	}
	targetPath, err := server.operationPath(filepath.Join(filepath.Dir(sourcePath), name))
	if err != nil {
		return err
	}
	if _, err := os.Lstat(targetPath); err == nil {
		return errors.New("目标名称已存在")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(sourcePath, targetPath)
}

func (server *Server) transferPaths(paths []string, destination string, move bool) error {
	if len(paths) == 0 {
		return errors.New("未选择文件")
	}
	destinationPath, err := server.operationPath(destination)
	if err != nil {
		return err
	}
	info, err := os.Stat(destinationPath)
	if err != nil || !info.IsDir() {
		return errors.New("目标目录不存在")
	}
	for _, selected := range paths {
		sourcePath, err := server.operationPath(selected)
		if err != nil {
			return err
		}
		if err := server.ensureNotRoot(sourcePath); err != nil {
			return err
		}
		targetPath, err := server.operationPath(filepath.Join(destinationPath, filepath.Base(sourcePath)))
		if err != nil {
			return err
		}
		if filepath.Clean(sourcePath) == filepath.Clean(targetPath) {
			return errors.New("源位置和目标位置相同")
		}
		relative, relErr := filepath.Rel(sourcePath, targetPath)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("不能将目录复制或移动到自身内部")
		}
		if _, err := os.Lstat(targetPath); err == nil {
			return fmt.Errorf("目标已存在：%s", filepath.Base(targetPath))
		} else if !os.IsNotExist(err) {
			return err
		}
		if move {
			if err := os.Rename(sourcePath, targetPath); err == nil {
				continue
			}
		}
		if err := copyTree(sourcePath, targetPath); err != nil {
			return err
		}
		if move {
			if err := os.RemoveAll(sourcePath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (server *Server) deletePaths(paths []string) error {
	if len(paths) == 0 {
		return errors.New("未选择文件")
	}
	for _, selected := range paths {
		path, err := server.operationPath(selected)
		if err != nil {
			return err
		}
		if err := server.ensureNotRoot(path); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("暂不支持复制符号链接")
	}
	if !info.IsDir() {
		return copyRegularFile(source, target, info.Mode())
	}
	if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		_ = os.RemoveAll(target)
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			_ = os.RemoveAll(target)
			return err
		}
	}
	return nil
}

func copyRegularFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	copied := false
	defer func() {
		_ = output.Close()
		if !copied {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	copied = true
	return nil
}
