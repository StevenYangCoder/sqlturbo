package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	domainconfig "sqlturbo/internal/domain/config"

	"github.com/pkg/sftp"
	"github.com/zeebo/xxh3"
	"golang.org/x/crypto/ssh"
)

const (
	uploadProgressMinInterval = 200 * time.Millisecond
	sftpUploadConcurrency     = 64
)

// ProgressFunc 用于回传上传或下载过程中的进度信息。
type ProgressFunc func(written int64, total int64)

// UploadResult 保存上传过程中顺手计算出来的本地哈希。
type UploadResult struct {
	LocalXXH3 string
}

// Client 封装 SSH 与 SFTP 连接，供应用层执行远程操作。
type Client struct {
	sshClient  *ssh.Client
	sftpClient *sftp.Client
}

// NewClient 基于数据库节点配置建立 SSH/SFTP 连接。
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

	sftpClient, err := sftp.NewClient(
		sshClient,
		sftp.UseConcurrentWrites(true),
		sftp.MaxConcurrentRequestsPerFile(sftpUploadConcurrency),
	)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("创建SFTP客户端失败：%w", err)
	}

	return &Client{
		sshClient:  sshClient,
		sftpClient: sftpClient,
	}, nil
}

// Close 释放 SSH 和 SFTP 连接资源。
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

// EnsureDir 确保远程目录存在。
func (c *Client) EnsureDir(remoteDir string) error {
	if err := c.sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("创建远程目录失败：%w", err)
	}
	return nil
}

// ListEntries 列出远程目录下的所有条目。
func (c *Client) ListEntries(remoteDir string) ([]os.FileInfo, error) {
	entries, err := c.sftpClient.ReadDir(remoteDir)
	if err != nil {
		return nil, fmt.Errorf("读取远程目录失败：%w", err)
	}
	return entries, nil
}

// RemoveFile 删除远程文件，文件不存在时直接忽略。
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

// CreateExclusiveFile 以独占方式创建远程文件。
func (c *Client) CreateExclusiveFile(remotePath string, content string) (bool, error) {
	file, err := c.sftpClient.OpenFile(remotePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		if isAlreadyExistsError(err) {
			return false, nil
		}
		return false, fmt.Errorf("独占创建远程文件失败：%w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, strings.NewReader(content)); err != nil {
		_ = c.sftpClient.Remove(remotePath)
		return false, fmt.Errorf("写入独占创建文件失败：%w", err)
	}

	return true, nil
}

// UploadFile 会把本地文件上传到远程目录，并持续回传进度。
func (c *Client) UploadFile(localPath string, remotePath string, onProgress ProgressFunc) error {
	_, err := c.UploadFileWithHash(localPath, remotePath, onProgress)
	return err
}

// UploadFileWithHash 会在上传时同步计算本地 xxHash3。
func (c *Client) UploadFileWithHash(localPath string, remotePath string, onProgress ProgressFunc) (UploadResult, error) {
	localFile, err := os.Open(localPath)
	if err != nil {
		return UploadResult{}, fmt.Errorf("打开本地文件失败：%w", err)
	}
	defer localFile.Close()

	info, err := localFile.Stat()
	if err != nil {
		return UploadResult{}, fmt.Errorf("读取本地文件信息失败：%w", err)
	}

	return c.uploadStream(localFile, info.Size(), remotePath, onProgress)
}

// UploadContent 会把内存中的内容上传到远程目录，并持续回传进度。
func (c *Client) UploadContent(remotePath string, content []byte, onProgress ProgressFunc) error {
	_, err := c.UploadContentWithHash(remotePath, content, onProgress)
	return err
}

// UploadContentWithHash 会在上传时同步计算内存内容的 xxHash3。
func (c *Client) UploadContentWithHash(remotePath string, content []byte, onProgress ProgressFunc) (UploadResult, error) {
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

// RunCommand 在远程主机上执行 shell 命令，并等待其完成。
func (c *Client) RunCommand(ctx context.Context, command string) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("创建SSH会话失败：%w", err)
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	errCh := make(chan error, 1)
	go func() {
		errCh <- session.Run(command)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf(
				"执行远程命令失败：%w；命令：%s；stdout：%s；stderr：%s",
				err,
				command,
				summarizeCommandOutput(stdout.String()),
				summarizeCommandOutput(stderr.String()),
			)
		}
		return nil
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	}
}

// RunCommandStream 流式读取远程命令的输出，并按行回调。
func (c *Client) RunCommandStream(ctx context.Context, command string, onLine func(line string)) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("创建SSH会话失败：%w", err)
	}
	defer session.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建标准输出管道失败：%w", err)
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建标准错误管道失败：%w", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var scanErr error
	var scanErrMutex sync.Mutex

	scan := func(reader io.Reader, out *bytes.Buffer) {
		defer func() {
			_ = recover()
		}()

		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			out.WriteString(line)
			out.WriteByte('\n')
			if onLine != nil {
				onLine(line)
			}
		}
		if err := scanner.Err(); err != nil {
			scanErrMutex.Lock()
			if scanErr == nil {
				scanErr = err
			}
			scanErrMutex.Unlock()
		}
	}

	if err := session.Start(command); err != nil {
		return fmt.Errorf("启动远程命令失败：%w", err)
	}

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		scan(stdoutPipe, &stdout)
	}()
	go func() {
		defer waitGroup.Done()
		scan(stderrPipe, &stderr)
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- session.Wait()
	}()

	select {
	case waitErr := <-waitCh:
		waitGroup.Wait()
		if waitErr != nil {
			return fmt.Errorf(
				"执行远程命令失败：%w；命令：%s；stdout：%s；stderr：%s",
				waitErr,
				command,
				summarizeCommandOutput(stdout.String()),
				summarizeCommandOutput(stderr.String()),
			)
		}
		scanErrMutex.Lock()
		defer scanErrMutex.Unlock()
		if scanErr != nil {
			return fmt.Errorf("读取远程命令输出失败：%w", scanErr)
		}
		return nil
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	}
}

// summarizeCommandOutput 会对过长的命令输出做截断。
func summarizeCommandOutput(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "(empty)"
	}

	const maxLen = 4000
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + "...(truncated)"
}

// uploadStream 会把输入流上传到远程文件，并在同一条读取流上计算 xxHash3。
func (c *Client) uploadStream(reader io.Reader, size int64, remotePath string, onProgress ProgressFunc) (UploadResult, error) {
	if err := c.sftpClient.MkdirAll(path.Dir(remotePath)); err != nil {
		return UploadResult{}, fmt.Errorf("创建远程目录失败：%w", err)
	}

	remoteFile, err := c.sftpClient.OpenFile(remotePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if err != nil {
		return UploadResult{}, fmt.Errorf("创建远程文件失败：%w", err)
	}
	defer remoteFile.Close()

	localHash := xxh3.New()
	teeReader := io.TeeReader(reader, localHash)
	progressReader := newProgressReader(teeReader, size, onProgress, uploadProgressMinInterval)
	written, err := remoteFile.ReadFromWithConcurrency(progressReader, sftpUploadConcurrency)
	if err != nil {
		_ = c.sftpClient.Remove(remotePath)
		return UploadResult{}, fmt.Errorf("上传文件失败：%w", err)
	}
	if size >= 0 && written != size {
		_ = c.sftpClient.Remove(remotePath)
		return UploadResult{}, fmt.Errorf("上传文件失败，写入字节数不匹配：written=%d,total=%d", written, size)
	}
	progressReader.ForceReport()

	return UploadResult{
		LocalXXH3: formatXXH3(localHash.Sum64()),
	}, nil
}

// ComputeRemoteXXH3 会计算远程文件的 xxHash3，并持续回传计算进度。
func (c *Client) ComputeRemoteXXH3(remotePath string, onProgress ProgressFunc) (string, error) {
	remoteFile, err := c.sftpClient.Open(remotePath)
	if err != nil {
		return "", fmt.Errorf("打开远程文件失败：%w", err)
	}
	defer remoteFile.Close()

	info, err := remoteFile.Stat()
	if err != nil {
		return "", fmt.Errorf("读取远程文件信息失败：%w", err)
	}

	remoteHash := xxh3.New()
	progressReader := newProgressReader(remoteFile, info.Size(), onProgress, uploadProgressMinInterval)
	if _, err := io.Copy(remoteHash, progressReader); err != nil {
		return "", fmt.Errorf("读取远程文件计算哈希失败：%w", err)
	}
	progressReader.ForceReport()

	return formatXXH3(remoteHash.Sum64()), nil
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

// progressReader 用于节流上传/下载进度回调。
type progressReader struct {
	reader         io.Reader
	total          int64
	onProgress     ProgressFunc
	minInterval    time.Duration
	written        int64
	lastReportTime time.Time
}

// newProgressReader 创建一个带节流的 reader 包装器。
func newProgressReader(reader io.Reader, total int64, onProgress ProgressFunc, minInterval time.Duration) *progressReader {
	return &progressReader{
		reader:         reader,
		total:          total,
		onProgress:     onProgress,
		minInterval:    minInterval,
		lastReportTime: time.Now(),
	}
}

// Read 读取数据并按节流规则上报进度。
func (r *progressReader) Read(p []byte) (int, error) {
	readSize, readErr := r.reader.Read(p)
	if readSize > 0 {
		r.written += int64(readSize)
		r.report(false)
	}
	if readErr == io.EOF {
		r.report(true)
	}
	return readSize, readErr
}

// ForceReport 强制刷新一次进度。
func (r *progressReader) ForceReport() {
	r.report(true)
}

// report 在满足节流间隔时触发回调。
func (r *progressReader) report(force bool) {
	if r.onProgress == nil {
		return
	}

	now := time.Now()
	if !force && now.Sub(r.lastReportTime) < r.minInterval {
		return
	}

	reported := r.written
	if r.total > 0 && reported > r.total {
		reported = r.total
	}
	r.lastReportTime = now
	r.onProgress(reported, r.total)
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

// formatXXH3 把 uint64 哈希值格式化成固定长度十六进制字符串。
func formatXXH3(sum uint64) string {
	return fmt.Sprintf("%016x", sum)
}
