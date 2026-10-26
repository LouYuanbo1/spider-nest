package web

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/johnfercher/maroto/v2/pkg/merge"
)

// defaultDownloadDir 返回平台默认的下载目录；取不到时回退为当前目录。
func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

// currentPageURL 返回页面当前 URL；拿不到时为空串。
func currentPageURL(ctx context.Context, page *rod.Page) string {
	info, err := page.Context(ctx).Info()
	if err != nil || info == nil {
		return ""
	}
	return info.URL
}

// filenameForbidden 把文件名中的非法字符统一替换为下划线。
var filenameForbidden = strings.NewReplacer(
	"/", "_",
	`\`, "_",
	":", "_",
	"*", "_",
	"?", "_",
	`"`, "_",
	"<", "_",
	">", "_",
	"|", "_",
	" ", "_",
)

// derivePDFName 依据 URL 生成一个可读且唯一的默认文件名：
// 取主机名 + 路径结尾片段，去掉非法字符，再拼接时间戳，避免重名覆盖。
func derivePDFName(rawURL string) string {
	base := "page"
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		host := filenameForbidden.Replace(strings.TrimSuffix(u.Hostname(), "."))
		tail := strings.Trim(path.Base(u.Path), "/")
		if tail != "" && tail != "." {
			base = host + "-" + filenameForbidden.Replace(tail)
		} else {
			base = host
		}
	}
	if base == "" {
		base = "page"
	}
	return fmt.Sprintf("%s-%s", base, time.Now().Format("20060102-150405"))
}

func (w *Web) pagePDF(ctx context.Context, gp *action.GetPDF) (string, error) {
	page := w.Page.Context(ctx)
	// GetPDF 内嵌 *proto.PagePrintToPDF 可为 nil（&GetPDF{Artifact:...} 构造时未设置）
	// 直接把 nil 传给 rod 的 Page.PDF 会在其内部解引用 req.TransferMode 而 panic，
	// 这里兜底分配一个零值请求以走 rod 的默认打印参数。
	printPdf := gp.PagePrintToPDF
	if printPdf == nil {
		printPdf = &proto.PagePrintToPDF{}
	}
	sr, err := page.PDF(printPdf)
	if err != nil {
		return "", err
	}
	defer sr.Close()

	dir := gp.Dir
	if dir == "" {
		dir = defaultDownloadDir()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	name := gp.Name
	if name == "" {
		name = derivePDFName(currentPageURL(ctx, page))
	}

	path := filepath.Join(dir, fmt.Sprintf("%s.pdf", name))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	// io.Copy 底层分块读取流并写入文件，全程常量内存，不会一次性载入整份 PDF。
	if _, err = io.Copy(f, sr); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// GetPDF 在网持有的页面上打印 PDF，并流式写入磁盘以避免大文件全部载入内存；
// 目录取 tp.Dir（为空时默认系统下载目录），文件名取 tp.Name
// （为空时按页面 URL + 时间戳自动生成，如 github.com-LouYuanbo1-ai-spider-20060102-150405.pdf），
// 返回生成的文件路径。
func (w *Web) GetPDF(ctx context.Context, gp *action.GetPDF) (string, error) {
	return w.timeoutResultWrapper(ctx, gp, w.pagePDF)
}

// MergePDFs 将多个已落盘的 PDF 文件合并为一份，并按 dst（Artifact）指定的目录与文件名保存。
// dst.Dir 为空时使用默认下载目录 defaultDownloadDir；dst.Name 为空时按"merged-时间戳"自动生成。
// 返回合并产物的完整文件路径。
func MergePDFs(ctx context.Context, dst action.Artifact, paths ...string) (string, error) {
	pdfs := make([][]byte, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("读取文件失败: %v", err)
		}
		pdfs = append(pdfs, data)
	}
	merged, err := merge.Bytes(pdfs...)
	if err != nil {
		return "", fmt.Errorf("合并 PDF 失败: %v", err)
	}

	dir := dst.Dir
	if dir == "" {
		dir = defaultDownloadDir()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %v", err)
	}

	name := dst.Name
	if name == "" {
		name = fmt.Sprintf("merged-%s", time.Now().Format("20060102-150405"))
	}

	out := filepath.Join(dir, fmt.Sprintf("%s.pdf", name))
	if err := os.WriteFile(out, merged, 0644); err != nil {
		return "", fmt.Errorf("写入合并文件失败: %v", err)
	}
	return out, nil
}
