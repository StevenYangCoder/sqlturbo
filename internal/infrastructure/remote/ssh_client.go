package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	domainconfig "sqlturbo/internal/domain/config"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ProgressFunc 用于回传上传或下载过程中的进度信息。
type ProgressFunc func(written int64, total int64)

// Client 封装 SSH 与 SFTP 连接，供应用层执行业务流程。
type Client struct {
	sshClient  *ssh.Client
	sftpClient *sftp.Client
}

// NewClient 会基于数据库节点配置建立 SSH/SFTP 连接。
func NewClient(database domainconfig.Database) (*Client, error) {
	sshConfig := &ssh.ClientConfig{
		User:            database.SSHUser(),
		Auth:            []ssh.AuthMethod{ssh.Password(database.SSHPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	address := fmt.Sprintf("%s:%d", database.SSHHost, database.SSHPort)
	sshClient, err := ssh.Dial("tcp", address, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("连接远程服务器失败：%w", err)
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("创建SFTP客户端失败：%w", err)
	}

	return &Client{
		sshClient:  sshClient,
		sftpClient: sftpClient,
	}, nil
}

// Close 会释放 SSH/SFTP 资源。
func (c *Client) Close() error {
	var closeErr error

	if c.sftpClient != nil {
		if err := c.sftpClient.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if c.sshClient != nil {
		if err := c.sshClient.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}

	return closeErr
}

// EnsureDir 会确保远程目录存在。
func (c *Client) EnsureDir(remoteDir string) error {
	if err := c.sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("创建远程目录失败：%w", err)
	}
	return nil
}

// ListEntries 会列出远程目录下的文件名称。
func (c *Client) ListEntries(remoteDir string) ([]os.FileInfo, error) {
	entries, err := c.sftpClient.ReadDir(remoteDir)
	if err != nil {
		return nil, fmt.Errorf("读取远程目录失败：%w", err)
	}
	return entries, nil
}

// RemoveFile 会删除远程文件。
func (c *Client) RemoveFile(remotePath string) error {
	if err := c.sftpClient.Remove(remotePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除远程文件失败：%w", err)
	}
	return nil
}

// WriteFile 会把文本内容写入远程文件。
func (c *Client) WriteFile(remotePath string, content string) error {
	return c.UploadContent(remotePath, []byte(content), nil)
}

// CreateExclusiveFile 会以排他方式创建远程文件；若文件已存在则返回 false。
func (c *Client) CreateExclusiveFile(remotePath string, content string) (bool, error) {
	file, err := c.sftpClient.OpenFile(remotePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		if isAlreadyExistsError(err) {
			return false, nil
		}
		return false, fmt.Errorf("排他创建远程文件失败：%w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, strings.NewReader(content)); err != nil {
		_ = c.sftpClient.Remove(remotePath)
		return false, fmt.Errorf("写入排他创建文件失败：%w", err)
	}

	return true, nil
}

// UploadFile 会把本地文件上传到远程目录，并持续回传进度。
func (c *Client) UploadFile(localPath string, remotePath string, onProgress ProgressFunc) error {
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败：%w", err)
	}
	defer localFile.Close()

	info, err := localFile.Stat()
	if err != nil {
		return fmt.Errorf("读取本地文件信息失败：%w", err)
	}

	return c.uploadStream(localFile, info.Size(), remotePath, onProgress)
}

// UploadContent 会把内存中的内容上传到远程目录，并持续回传进度。
func (c *Client) UploadContent(remotePath string, content []byte, onProgress ProgressFunc) error {
	reader := bytes.NewReader(content)
	return c.uploadStream(reader, int64(len(content)), remotePath, onProgress)
}

// DownloadFile 会从远程目录下载文件到本地，并持续回传进度。
func (c *Client) DownloadFile(remotePath string, localPath string, onProgress ProgressFunc) error {
	remoteFile, err := c.sftpClient.Open(remotePath)
	if err != nil {
		return fmt.Errorf("打开远程文件失败：%w", err)
	}
	defer remoteFile.Close()

	info, err := remoteFile.Stat()
	if err != nil {
		return fmt.Errorf("读取远程文件信息失败：%w", err)
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("创建本地目录失败：%w", err)
	}

	localFile, err := os.OpenFile(localPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("创建本地文件失败：%w", err)
	}
	defer localFile.Close()

	if err := copyWithProgress(localFile, remoteFile, info.Size(), onProgress); err != nil {
		return fmt.Errorf("下载文件失败：%w", err)
	}
	return nil
}

// RunCommand 会在远程主机执行 shell 命令，并等待执行完成。
func (c *Client) RunCommand(ctx context.Context, command string) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("创建SSH会话失败：%w", err)
	}
	defer session.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- session.Run(command)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("执行远程命令失败：%w", err)
		}
		return nil
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	}
}

// uploadStream 会把输入流上传到远程文件。
func (c *Client) uploadStream(reader io.Reader, size int64, remotePath string, onProgress ProgressFunc) error {
	if err := c.sftpClient.MkdirAll(path.Dir(remotePath)); err != nil {
		return fmt.Errorf("创建远程目录失败：%w", err)
	}

	remoteFile, err := c.sftpClient.OpenFile(remotePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("创建远程文件失败：%w", err)
	}
	defer remoteFile.Close()

	if err := copyWithProgress(remoteFile, reader, size, onProgress); err != nil {
		return fmt.Errorf("上传文件失败：%w", err)
	}
	return nil
}

// copyWithProgress 会执行流复制，并在每次写入后回调当前进度。
func copyWithProgress(dst io.Writer, src io.Reader, total int64, onProgress ProgressFunc) error {
	buffer := make([]byte, 32*1024)
	var written int64

	for {
		readSize, readErr := src.Read(buffer)
		if readSize > 0 {
			writeSize, writeErr := dst.Write(buffer[:readSize])
			written += int64(writeSize)
			if onProgress != nil {
				onProgress(written, total)
			}
			if writeErr != nil {
				return writeErr
			}
			if writeSize != readSize {
				return io.ErrShortWrite
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				if onProgress != nil {
					onProgress(total, total)
				}
				return nil
			}
			return readErr
		}
	}
}

// isAlreadyExistsError 判断远程创建失败是否因为文件已存在。
func isAlreadyExistsError(err error) bool {
	if os.IsExist(err) {
		return true
	}

	var statusErr *sftp.StatusError
	if errors.As(err, &statusErr) && statusErr.Code == 11 {
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file already exists") || strings.Contains(message, "already exists")
}
