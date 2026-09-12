package server

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxArchiveEntries = 100000
	maxExtractedBytes = int64(1 << 30)
)

type archiveRequest struct {
	Action      string   `json:"action"`
	Path        string   `json:"path"`
	Paths       []string `json:"paths"`
	Destination string   `json:"destination"`
	Name        string   `json:"name"`
}

func (server *Server) archiveFile(w http.ResponseWriter, r *http.Request) {
	if !server.authenticated(r) {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in archiveRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "请求无效", http.StatusBadRequest)
		return
	}
	var err error
	switch in.Action {
	case "extract":
		err = server.extractArchive(in.Path, in.Destination)
	case "compress":
		err = server.createZip(in.Paths, in.Destination, in.Name)
	default:
		http.Error(w, "不支持的压缩操作", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (server *Server) extractArchive(requestedArchive, requestedDestination string) error {
	lower := strings.ToLower(requestedArchive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return server.extractZip(requestedArchive, requestedDestination)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return server.extractTarGz(requestedArchive, requestedDestination)
	default:
		return fmt.Errorf("目前仅支持解压 ZIP、TAR.GZ 和 TGZ 文件")
	}
}

func extractionTarget(destination, name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("压缩包包含不安全路径")
	}
	destination = filepath.Clean(destination)
	target := filepath.Clean(filepath.Join(destination, name))
	prefix := destination + string(os.PathSeparator)
	if target != destination && !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("压缩包包含越界路径")
	}
	return target, nil
}

func (server *Server) extractTarGz(requestedArchive, requestedDestination string) error {
	archivePath, err := server.filePath(requestedArchive)
	if err != nil {
		return fmt.Errorf("压缩包路径无效")
	}
	destination, err := server.filePath(requestedDestination)
	if err != nil {
		return fmt.Errorf("目标目录无效")
	}
	info, err := os.Stat(destination)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("目标目录不存在")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("无法打开 TAR.GZ 文件")
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("无法读取 GZIP 数据")
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries := 0
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 TAR.GZ 文件失败")
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("压缩包文件数量超过限制")
		}
		if header.Size < 0 || header.Size > maxExtractedBytes-total {
			return fmt.Errorf("解压后大小超过 1 GB 限制")
		}
		total += header.Size
		target, err := extractionTarget(destination, header.Name)
		if err != nil {
			return err
		}
		mode := os.FileMode(header.Mode).Perm()
		switch header.Typeflag {
		case tar.TypeDir:
			if mode == 0 {
				mode = 0755
			}
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if mode == 0 {
				mode = 0644
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(dst, reader)
			closeErr := dst.Close()
			if copyErr != nil || closeErr != nil {
				return fmt.Errorf("写入解压文件失败")
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("压缩包包含不支持的链接")
		default:
			return fmt.Errorf("压缩包包含不支持的文件类型")
		}
	}
	return nil
}

func (server *Server) extractZip(requestedArchive, requestedDestination string) error {
	archivePath, err := server.filePath(requestedArchive)
	if err != nil || !strings.EqualFold(filepath.Ext(archivePath), ".zip") {
		return fmt.Errorf("目前仅支持解压 ZIP 文件")
	}
	destination, err := server.filePath(requestedDestination)
	if err != nil {
		return fmt.Errorf("目标目录无效")
	}
	info, err := os.Stat(destination)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("目标目录不存在")
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("无法打开 ZIP 文件")
	}
	defer zr.Close()
	if len(zr.File) > maxArchiveEntries {
		return fmt.Errorf("压缩包文件数量超过限制")
	}
	destination = filepath.Clean(destination)
	prefix := destination + string(os.PathSeparator)
	var total int64
	for _, item := range zr.File {
		if item.UncompressedSize64 > uint64(maxExtractedBytes-total) {
			return fmt.Errorf("解压后大小超过 1 GB 限制")
		}
		total += int64(item.UncompressedSize64)
		name := filepath.Clean(filepath.FromSlash(item.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("压缩包包含不安全路径")
		}
		target := filepath.Clean(filepath.Join(destination, name))
		if target != destination && !strings.HasPrefix(target, prefix) {
			return fmt.Errorf("压缩包包含越界路径")
		}
		mode := item.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("压缩包包含不支持的符号链接")
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(target, mode.Perm()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		src, err := item.Open()
		if err != nil {
			return fmt.Errorf("读取压缩包失败")
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		src.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("写入解压文件失败")
		}
	}
	return nil
}

func (server *Server) createZip(requestedPaths []string, requestedDestination, name string) error {
	if len(requestedPaths) == 0 {
		return fmt.Errorf("请选择要压缩的文件")
	}
	destination, err := server.filePath(requestedDestination)
	if err != nil {
		return fmt.Errorf("目标目录无效")
	}
	info, err := os.Stat(destination)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("目标目录不存在")
	}
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "" {
		return fmt.Errorf("压缩包名称无效")
	}
	if !strings.EqualFold(filepath.Ext(name), ".zip") {
		name += ".zip"
	}
	finalPath := filepath.Join(destination, name)
	if _, err := os.Stat(finalPath); err == nil {
		return fmt.Errorf("同名压缩包已存在")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("无法检查目标文件")
	}
	tmp, err := os.CreateTemp(destination, ".termdock-*.zip")
	if err != nil {
		return fmt.Errorf("无法创建压缩包")
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	zw := zip.NewWriter(tmp)
	for _, requested := range requestedPaths {
		sourcePath, err := server.filePath(requested)
		if err != nil {
			zw.Close()
			return fmt.Errorf("源路径无效")
		}
		baseParent := filepath.Dir(sourcePath)
		err = filepath.Walk(sourcePath, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			rel, err := filepath.Rel(baseParent, path)
			if err != nil {
				return err
			}
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(rel)
			if info.IsDir() {
				header.Name += "/"
			} else {
				header.Method = zip.Deflate
			}
			aw, err := zw.CreateHeader(header)
			if err != nil || info.IsDir() {
				return err
			}
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(aw, src)
			src.Close()
			return err
		})
		if err != nil {
			zw.Close()
			return fmt.Errorf("压缩失败: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("完成压缩包失败")
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("保存压缩包失败")
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("保存压缩包失败: %w", err)
	}
	committed = true
	return nil
}
